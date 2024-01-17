package readline

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

type AutoCompleter interface {
	// Do Readline will pass the whole line and current offset to it
	// Completer need to pass all the candidates, and how long they shared the same characters in line
	// Example:
	//   [go, git, git-shell, grep]
	//   Do("g", 1) => ["o", "it", "it-shell", "rep"], 1
	//   Do("gi", 2) => ["t", "t-shell"], 2
	//   Do("git", 3) => ["", "-shell"], 3
	Do(line []rune, pos int) (newLine, commentLine [][]rune, length int)
}

type TabCompleter struct{}

func (t *TabCompleter) Do([]rune, int) ([][]rune, [][]rune, int) {
	return [][]rune{[]rune("\t")}, nil, 0
}

type opCompleter struct {
	// 与readline.operation.w相同。
	w io.Writer
	// 与readline.operation.buf 相同
	op *Operation
	// 终端屏幕的宽度。
	width int

	// 按下tab之后，其有候选项(且大于1)时，值此值会设置为true。
	inCompleteMode bool
	// 已经列出了候选项，再次按tab，会在候选项中移动。
	inSelectMode bool
	candidate    [][]rune
	// add
	candidateComments [][]rune
	// 按下tab时，光标左边的所有字符串。
	candidateSource []rune
	// Do 的返回值
	// 前缀字符的长度。即输入与候选项的共同前缀的长度。
	// 比如：已经输入了vi然后按tab，有候选项 vim vim2 ，那么这个candidateOff 的值
	// 就是2
	candidateOff int
	// 第几个候选项被高亮，即当前选择的，从0开始。
	candidateChoise int
	// 候选项排成几列
	candidateColNum int
}

func newOpCompleter(w io.Writer, op *Operation, width int) *opCompleter {
	return &opCompleter{
		w:     w,
		op:    op,
		width: width,
	}
}

func (o *opCompleter) doSelect() {
	if len(o.candidate) == 1 {
		o.op.buf.WriteRunes(o.candidate[0])
		o.ExitCompleteMode(false)
		return
	}
	o.nextCandidate(1)
	o.CompleteRefresh()
}

func (o *opCompleter) nextCandidate(i int) {
	o.candidateChoise += i
	o.candidateChoise = o.candidateChoise % len(o.candidate)
	if o.candidateChoise < 0 {
		o.candidateChoise = len(o.candidate) + o.candidateChoise
	}
}

func (o *opCompleter) OnComplete() bool {
	if o.width == 0 {
		return false
	}
	if o.IsInCompleteSelectMode() {
		o.doSelect()
		return true
	}

	buf := o.op.buf
	rs := buf.Runes()

	if o.IsInCompleteMode() && o.candidateSource != nil && runes.Equal(rs, o.candidateSource) {
		o.EnterCompleteSelectMode()
		o.doSelect()
		return true
	}

	o.ExitCompleteSelectMode()
	o.candidateSource = rs
	newLines, commentLines, offset := o.op.cfg.AutoComplete.Do(rs, buf.idx)
	if len(newLines) == 0 {
		o.ExitCompleteMode(false)
		return true
	}

	// only Aggregate candidates in non-complete mode
	if !o.IsInCompleteMode() {
		if len(newLines) == 1 {
			buf.WriteRunes(newLines[0])
			o.ExitCompleteMode(false)
			return true
		}

		same, size := runes.Aggregate(newLines)
		if size > 0 {
			buf.WriteRunes(same)
			o.ExitCompleteMode(false)
			return true
		}
	}

	o.EnterCompleteMode(offset, newLines, commentLines)
	return true
}

func (o *opCompleter) IsInCompleteSelectMode() bool {
	return o.inSelectMode
}

func (o *opCompleter) IsInCompleteMode() bool {
	return o.inCompleteMode
}

func (o *opCompleter) HandleCompleteSelect(r rune) bool {
	next := true
	switch r {
	case CharEnter, CharCtrlJ:
		next = false
		o.op.buf.WriteRunes(o.op.candidate[o.op.candidateChoise])
		o.ExitCompleteMode(false)
	case CharLineStart:
		num := o.candidateChoise % o.candidateColNum
		o.nextCandidate(-num)
	case CharLineEnd:
		num := o.candidateColNum - o.candidateChoise%o.candidateColNum - 1
		o.candidateChoise += num
		if o.candidateChoise >= len(o.candidate) {
			o.candidateChoise = len(o.candidate) - 1
		}
	case CharBackspace:
		o.ExitCompleteSelectMode()
		next = false
	case CharTab, CharForward:
		o.doSelect()
	case CharBell, CharInterrupt:
		o.ExitCompleteMode(true)
		next = false
	case CharNext:
		tmpChoise := o.candidateChoise + o.candidateColNum
		if tmpChoise >= o.getMatrixSize() {
			tmpChoise -= o.getMatrixSize()
		} else if tmpChoise >= len(o.candidate) {
			tmpChoise += o.candidateColNum
			tmpChoise -= o.getMatrixSize()
		}
		o.candidateChoise = tmpChoise
	case CharBackward:
		o.nextCandidate(-1)
	case CharPrev:
		tmpChoise := o.candidateChoise - o.candidateColNum
		if tmpChoise < 0 {
			tmpChoise += o.getMatrixSize()
			if tmpChoise >= len(o.candidate) {
				tmpChoise -= o.candidateColNum
			}
		}
		o.candidateChoise = tmpChoise
	default:
		next = false
		o.ExitCompleteSelectMode()
	}
	if next {
		o.CompleteRefresh()
		return true
	}
	return false
}

func (o *opCompleter) getMatrixSize() int {
	line := len(o.candidate) / o.candidateColNum
	if len(o.candidate)%o.candidateColNum != 0 {
		line++
	}
	return line * o.candidateColNum
}

func (o *opCompleter) OnWidthChange(newWidth int) {
	o.width = newWidth
}

func (o *opCompleter) CompleteRefresh() {
	if !o.inCompleteMode {
		return
	}
	// 光标所在行后面还有多少行+1。
	lineCnt := o.op.buf.CursorLineCount()
	// 候选项中最大宽度是多少
	colWidth := 0
	for i, c := range o.candidate {
		w := runes.WidthAll(c)
		// comment add here
		w += runes.WidthAll(o.candidateComments[i])
		if w > colWidth {
			colWidth = w
		}
	}
	// 候选项中最大宽度 + 输入中与原始候选项的公共前缀的长度。
	colWidth += o.candidateOff + 1
	// same是自动填充之前，光标左边的字符串，不包括prompt。
	same := o.op.buf.RuneSlice(-o.candidateOff)

	// -1 to avoid reach the end of line
	width := o.width - 1
	colNum := width / colWidth
	if colNum != 0 {
		colWidth += (width - (colWidth * colNum)) / colNum
	}

	o.candidateColNum = colNum
	buf := bufio.NewWriter(o.w)
	// 移动到输入形成的行的后面一个行，这是接下来候选项输入的起始位置。
	buf.Write(bytes.Repeat([]byte("\n"), lineCnt))

	colIdx := 0
	lines := 1
	// 清空光标所在位置+后面直到页面末尾
	buf.WriteString("\033[J")
	for idx, c := range o.candidate {
		// c是当前tab应该选中的候选项
		inSelect := idx == o.candidateChoise && o.IsInCompleteSelectMode()
		if inSelect {
			// 对选中的候选项进行高亮处理
			buf.WriteString("\033[30;47m")
		}
		// 写入共同部分。
		buf.WriteString(string(same))
		// 写入去掉共同部分的候选项。
		buf.WriteString(string(c))
		// 写入候选项的注释
		if len(o.candidateComments[idx]) > 0 {
			buf.WriteString("\033[90m" + string(o.candidateComments[idx]) + "\033[39m")
		}
		// 填充到列宽
		buf.Write(bytes.Repeat([]byte(" "), colWidth-runes.WidthAll(c)-runes.WidthAll(same)-runes.WidthAll(o.candidateComments[idx])))

		if inSelect {
			// 清空对选中候选项的特色处理
			buf.WriteString("\033[0m")
		}

		colIdx++
		if colIdx == colNum {
			// 当前候选项已经位于最后一列，应该换行了
			buf.WriteString("\n")
			lines++
			colIdx = 0
		}
	}
	// move back
	// 移动会光标原来所在的行。
	fmt.Fprintf(buf, "\033[%dA\r", lineCnt-1+lines)
	// 移动光标到原来的位置。
	fmt.Fprintf(buf, "\033[%dC", o.op.buf.idx+o.op.buf.PromptLen())
	// 将候选项列表输出到终端。
	buf.Flush()
}

func (o *opCompleter) aggCandidate(candidate [][]rune) int {
	offset := 0
	for i := 0; i < len(candidate[0]); i++ {
		for j := 0; j < len(candidate)-1; j++ {
			if i > len(candidate[j]) {
				goto aggregate
			}
			if candidate[j][i] != candidate[j+1][i] {
				goto aggregate
			}
		}
		offset = i
	}
aggregate:
	return offset
}

func (o *opCompleter) EnterCompleteSelectMode() {
	o.inSelectMode = true
	o.candidateChoise = -1
	o.CompleteRefresh()
}

// EnterCompleteMode offset 光标在补充完候选项之后所在的位置。
func (o *opCompleter) EnterCompleteMode(offset int, candidate, comments [][]rune) {
	o.inCompleteMode = true
	o.candidate = candidate
	o.candidateComments = comments
	o.candidateOff = offset
	o.CompleteRefresh()
}

func (o *opCompleter) ExitCompleteSelectMode() {
	o.inSelectMode = false
	o.candidate = nil
	o.candidateComments = nil
	o.candidateChoise = -1
	o.candidateOff = -1
	o.candidateSource = nil
}

func (o *opCompleter) ExitCompleteMode(revent bool) {
	o.inCompleteMode = false
	o.ExitCompleteSelectMode()
}
