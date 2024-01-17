package readline

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

type Terminal struct {
	m         sync.Mutex
	cfg       *Config
	outchan   chan rune
	closed    int32
	stopChan  chan struct{}
	kickChan  chan struct{}
	wg        sync.WaitGroup
	isReading int32
	sleeping  int32

	sizeChan chan string
}

func NewTerminal(cfg *Config) (*Terminal, error) {
	if err := cfg.Init(); err != nil {
		return nil, err
	}
	t := &Terminal{
		cfg:      cfg,
		kickChan: make(chan struct{}, 1),
		outchan:  make(chan rune),
		stopChan: make(chan struct{}, 1),
		sizeChan: make(chan string, 1),
	}

	go t.ioloop()
	return t, nil
}

// SleepToResume will sleep myself, and return only if I'm resumed.
func (t *Terminal) SleepToResume() {
	if !atomic.CompareAndSwapInt32(&t.sleeping, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&t.sleeping, 0)

	t.ExitRawMode()
	ch := WaitForResume()
	SuspendMe()
	<-ch
	t.EnterRawMode()
}

func (t *Terminal) EnterRawMode() (err error) {
	return t.cfg.FuncMakeRaw()
}

func (t *Terminal) ExitRawMode() (err error) {
	return t.cfg.FuncExitRaw()
}

func (t *Terminal) Write(b []byte) (int, error) {
	return t.cfg.Stdout.Write(b)
}

// WriteStdin prefill the next Stdin fetch
// Next time you call ReadLine() this value will be writen before the user input
func (t *Terminal) WriteStdin(b []byte) (int, error) {
	return t.cfg.StdinWriter.Write(b)
}

type termSize struct {
	left int
	top  int
}

func (t *Terminal) GetOffset(f func(offset string)) {
	go func() {
		f(<-t.sizeChan)
	}()
	t.Write([]byte("\033[6n"))
}

func (t *Terminal) Print(s string) {
	fmt.Fprintf(t.cfg.Stdout, "%s", s)
}

func (t *Terminal) PrintRune(r rune) {
	fmt.Fprintf(t.cfg.Stdout, "%c", r)
}

func (t *Terminal) Readline() *Operation {
	return NewOperation(t, t.cfg)
}

// ReadRune return rune(0) if meet EOF
func (t *Terminal) ReadRune() rune {
	ch, ok := <-t.outchan
	if !ok {
		return rune(0)
	}
	return ch
}

func (t *Terminal) IsReading() bool {
	return atomic.LoadInt32(&t.isReading) == 1
}

func (t *Terminal) KickRead() {
	select {
	case t.kickChan <- struct{}{}:
	default:
	}
}

// Terminal 从STDIN中读取内容，如果是控制字符序列通过CTRL+其它字符输入的，转为单个rune。
// 比如通过键盘输入ctrl+D。从终端中读取到的是 27(ESC)、[、D 这3个rune字符，其会将其转换为
// CharBackward 后发送给 Operation 的ioloop。
func (t *Terminal) ioloop() {
	t.wg.Add(1)
	defer func() {
		t.wg.Done()
		close(t.outchan)
	}()

	type readRune struct {
		r   rune
		err error
	}
	var (
		// 如果从STDIN读取一个rune是ESC，这此值会被设置为true。
		isEscape    bool
		isEscapeEx  bool
		isEscapeSS3 bool
		// 每次成功发送一个非终端/换行字符后，此值就会被设置为true。
		// 初始此值设置为false，terminal停靠在kickChan通道上，由Operation
		// 在需要读取字符时负责唤醒。
		expectNextChar bool
		// recvR          = make(chan *readRune)
	)

	buf := bufio.NewReader(t.getStdin())
	/*
		go func() {
			for {
				r, _, err := buf.ReadRune()
				select {
				case recvR <- &readRune{r: r, err: err}:
				case <-t.stopChan:
					return
				}
			}
		}()
	*/
	for {
		if !expectNextChar {
			atomic.StoreInt32(&t.isReading, 0)
			select {
			case <-t.kickChan:
				atomic.StoreInt32(&t.isReading, 1)
			case <-t.stopChan:
				return
			}
		}
		expectNextChar = false
		/*
			var r rune
			var err error
			var recv *readRune
			select {
			case recv = <-recvR:
				r = recv.r
				err = recv.err
			case <-t.stopChan:
				return
			}
		*/

		r, _, err := buf.ReadRune()
		if err != nil {
			if strings.Contains(err.Error(), "interrupted system call") {
				expectNextChar = true
				continue
			}
			break
		}

		if isEscape {
			isEscape = false
			if r == CharEscapeEx {
				// ^][
				expectNextChar = true
				isEscapeEx = true
				continue
			} else if r == CharO {
				// ^]O
				expectNextChar = true
				isEscapeSS3 = true
				continue
			}
			r = escapeKey(r, buf)
		} else if isEscapeEx {
			isEscapeEx = false
			if key := readEscKey(r, buf); key != nil {
				r = escapeExKey(key)
				// offset
				if key.typ == 'R' {
					if _, _, ok := key.Get2(); ok {
						select {
						case t.sizeChan <- key.attr:
						default:
						}
					}
					expectNextChar = true
					continue
				}
			}
			if r == 0 {
				expectNextChar = true
				continue
			}
		} else if isEscapeSS3 {
			isEscapeSS3 = false
			if key := readEscKey(r, buf); key != nil {
				r = escapeSS3Key(key)
			}
			if r == 0 {
				expectNextChar = true
				continue
			}
		}

		expectNextChar = true
		switch r {
		case CharEsc:
			if t.cfg.VimMode {
				select {
				case t.outchan <- r:
					break
				case <-t.stopChan:
					return
				}
			}
			isEscape = true
		case CharInterrupt, CharEnter, CharCtrlJ, CharDelete:
			expectNextChar = false
			fallthrough
		default:
			// 当按^@时会像terminal发送单单一个0，而Operation认为0是退出逻辑会通过关闭
			// stopChan来通知此循环，如果expectNextChar为true，则接下来不会在stopChan上停靠。
			// if r == 0 {
			// 	expectNextChar = false
			// }
			select {
			case <-t.stopChan:
				return
			case t.outchan <- r:
			}
		}
	}

}

func (t *Terminal) Bell() {
	fmt.Fprintf(t, "%c", CharBell)
}

func (t *Terminal) Close() error {
	if atomic.SwapInt32(&t.closed, 1) != 0 {
		return nil
	}
	if closer, ok := t.cfg.Stdin.(io.Closer); ok {
		closer.Close()
	}
	close(t.stopChan)
	t.wg.Wait()
	return t.ExitRawMode()
}

func (t *Terminal) GetConfig() *Config {
	t.m.Lock()
	cfg := *t.cfg
	t.m.Unlock()
	return &cfg
}

func (t *Terminal) getStdin() io.Reader {
	t.m.Lock()
	r := t.cfg.Stdin
	t.m.Unlock()
	return r
}

func (t *Terminal) SetConfig(c *Config) error {
	if err := c.Init(); err != nil {
		return err
	}
	t.m.Lock()
	t.cfg = c
	t.m.Unlock()
	return nil
}
