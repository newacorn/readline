package readline

import (
	"bufio"
	"bytes"
	"container/list"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

var (
	isWindows = false
)

const (
	// CharLineStart move the cursor to the top left corner of screen
	// 将光标移动到终端当前页的左上角。
	// \033[H
	CharLineStart = 1
	// CharBackward vim b
	CharBackward = 2
	// CharInterrupt Control-C is often used to interrupt a program or process, a standard that started with Dec operating systems
	// ^C
	CharInterrupt = 3
	// CharDelete Delete删除光标处的字符光标位置不移动
	// \033[3~
	// 通过^D输入。
	// 如果buf中没有内容，会将此输入作为EOF来处理。
	CharDelete = 4
	// CharLineEnd 将光标移动到输入的末尾
	// \033[F
	CharLineEnd = 5
	// CharForward \033[C 光标向前移动一个位置
	// vim l
	CharForward = 6
	// CharBell "\a" Bell，终端会发出声音。
	//  It is usually used to indicate a problem where a wrong character has been typed.
	CharBell = 7
	// CharCtrlH BS 将光标像左移动一个位置
	// 此包会删除移动后的光标位置处的字符。同 Ascii 127。
	// 通过^H输入
	CharCtrlH = 8
	// CharTab Ascii Tab
	CharTab = 9
	// CharCtrlJ 换行 Ascii LF。同 CharEnter 功能一致。
	// 通过 ^J 输入
	CharCtrlJ = 10
	// CharKill 光标处到输入末尾字符都会被删除。
	// 通过^K输入。
	CharKill = 11
	// CharCtrlL 清除终端中整页内容。并重新输出buf中的内容。
	// 通过^L输入
	// ASCII 12
	CharCtrlL = 12
	// CharEnter 回车键
	// ASCII 13
	CharEnter = 13
	// CharNext \033[B
	// 将后一个历史记录替换当前输入。
	// 通过^N输入
	// ASCII 14
	CharNext = 14
	// CharPrev \033[A
	// 将前一个历史记录替换当前输入。
	// 通过^P输入
	// ASCII 16
	CharPrev = 16
	// CharBckSearch Ascii DC2
	// 通过^R输入
	CharBckSearch = 18
	// CharFwdSearch 通过^S输入
	CharFwdSearch = 19
	// CharTranspose 通过^T输入
	// 将光标处的字符与其左边的字符位置互换，并将光标向右移动一个位置。
	CharTranspose = 20
	// CharCtrlU 通过^U输入，与^K相反清空光标前面的所有字符，不清除光标位置处的字符。
	CharCtrlU = 21
	// CharCtrlW 通过^W输入。
	// 同 MetaBackspace 用来删除光标左边的单词部分。光标位置上的字符保留。整体向左移动。
	// 如果光标处不是单词字符，则删除其左边的字符直到删除完一个单词。
	CharCtrlW = 23
	// CharCtrlY 通过^Y输入
	// 将上次删除的字符串。插入到光标左边的位置。光标依旧在其原来的字符上。
	CharCtrlY = 25
	// CharCtrlZ 通过^Z输入
	// 执行 SleepToResume
	CharCtrlZ = 26
	// CharEsc ASCII ESC
	// 使用 ^[输入。
	// 在vim 模式中使用作为退出编辑模式.
	CharEsc = 27
	// CharO ASCII 79
	// ^]O
	// 	expectNextChar = true
	//  isEscapeSS3 = true
	CharO = 79
	// CharEscapeEx ^][
	// 	expectNextChar = true
	//				isEscapeEx = true
	CharEscapeEx = 91
	// CharBackspace delete the previous character in the line mode
	// Ascii码 127
	CharBackspace = 127
)

const (
	MetaBackward rune = -iota - 1
	MetaForward
	MetaDelete
	MetaBackspace
	MetaTranspose
)

// WaitForResume need to call before current process got suspend.
// It will run a ticker until a long duration is occurs,
// which means this process is resumed.
func WaitForResume() chan struct{} {
	ch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		t := time.Now()
		wg.Done()
		for {
			now := <-ticker.C
			if now.Sub(t) > 100*time.Millisecond {
				break
			}
			t = now
		}
		ticker.Stop()
		ch <- struct{}{}
	}()
	wg.Wait()
	return ch
}

func Restore(fd int, state *State) error {
	err := restoreTerm(fd, state)
	if err != nil {
		// errno 0 means everything is ok :)
		if err.Error() == "errno 0" {
			return nil
		} else {
			return err
		}
	}
	return nil
}

func IsPrintable(key rune) bool {
	isInSurrogateArea := key >= 0xd800 && key <= 0xdbff
	return key >= 32 && !isInSurrogateArea
}

// translate Esc[X
func escapeExKey(key *escapeKeyPair) rune {
	var r rune
	switch key.typ {
	case 'D':
		r = CharBackward
	case 'C':
		r = CharForward
	case 'A':
		r = CharPrev
	case 'B':
		r = CharNext
	case 'H':
		r = CharLineStart
	case 'F':
		r = CharLineEnd
	case '~':
		if key.attr == "3" {
			r = CharDelete
		}
	default:
	}
	return r
}

// translate EscOX SS3 codes for up/down/etc.
func escapeSS3Key(key *escapeKeyPair) rune {
	var r rune
	switch key.typ {
	case 'D':
		r = CharBackward
	case 'C':
		r = CharForward
	case 'A':
		r = CharPrev
	case 'B':
		r = CharNext
	case 'H':
		r = CharLineStart
	case 'F':
		r = CharLineEnd
	default:
	}
	return r
}

type escapeKeyPair struct {
	attr string
	typ  rune
}

func (e *escapeKeyPair) Get2() (int, int, bool) {
	sp := strings.Split(e.attr, ";")
	if len(sp) < 2 {
		return -1, -1, false
	}
	s1, err := strconv.Atoi(sp[0])
	if err != nil {
		return -1, -1, false
	}
	s2, err := strconv.Atoi(sp[1])
	if err != nil {
		return -1, -1, false
	}
	return s1, s2, true
}

func readEscKey(r rune, reader *bufio.Reader) *escapeKeyPair {
	p := escapeKeyPair{}
	buf := bytes.NewBuffer(nil)
	for {
		if r == ';' {
		} else if unicode.IsNumber(r) {
		} else {
			p.typ = r
			break
		}
		buf.WriteRune(r)
		r, _, _ = reader.ReadRune()
	}
	p.attr = buf.String()
	return &p
}

// translate EscX to Meta+X
func escapeKey(r rune, reader *bufio.Reader) rune {
	switch r {
	case 'b':
		r = MetaBackward
	case 'f':
		r = MetaForward
	case 'd':
		r = MetaDelete
	case CharTranspose:
		r = MetaTranspose
	case CharBackspace:
		r = MetaBackspace
	case 'O':
		d, _, _ := reader.ReadRune()
		switch d {
		case 'H':
			r = CharLineStart
		case 'F':
			r = CharLineEnd
		default:
			reader.UnreadRune()
		}
	case CharEsc:

	}
	return r
}

func SplitByLine(start, screenWidth int, rs []rune) []string {
	var ret []string
	buf := bytes.NewBuffer(nil)
	currentWidth := start
	for _, r := range rs {
		w := runes.Width(r)
		currentWidth += w
		buf.WriteRune(r)
		if currentWidth >= screenWidth {
			ret = append(ret, buf.String())
			buf.Reset()
			currentWidth = 0
		}
	}
	ret = append(ret, buf.String())
	return ret
}

// LineCount calculate how many lines for N character
func LineCount(screenWidth, w int) int {
	r := w / screenWidth
	if w%screenWidth != 0 {
		r++
	}
	return r
}

func IsWordBreak(i rune) bool {
	switch {
	case i >= 'a' && i <= 'z':
	case i >= 'A' && i <= 'Z':
	case i >= '0' && i <= '9':
	default:
		return true
	}
	return false
}

func GetInt(s []string, def int) int {
	if len(s) == 0 {
		return def
	}
	c, err := strconv.Atoi(s[0])
	if err != nil {
		return def
	}
	return c
}

type RawMode struct {
	state *State
	// curTermios *Termios
}

func (r *RawMode) Enter() (err error) {
	r.state /*r.curTermios ,*/, err = MakeRaw(GetStdin())
	return err
}

func (r *RawMode) Exit() error {
	if r.state == nil {
		return nil
	}
	return Restore(GetStdin(), r.state)
}

// -----------------------------------------------------------------------------

func sleep(n int) {
	Debug(n)
	time.Sleep(2000 * time.Millisecond)
}

// print a linked list to Debug()
func debugList(l *list.List) {
	idx := 0
	for e := l.Front(); e != nil; e = e.Next() {
		Debug(idx, fmt.Sprintf("%+v", e.Value))
		idx++
	}
}

// Debug append log info to another file
func Debug(o ...interface{}) {
	f, _ := os.OpenFile("debug.tmp", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	fmt.Fprintln(f, o...)
	f.Close()
}

func CaptureExitSignal(f func()) {
	cSignal := make(chan os.Signal, 1)
	signal.Notify(cSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range cSignal {
			f()
		}
	}()
}
