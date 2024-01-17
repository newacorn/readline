package readline

import (
	"bytes"
	"strings"
)

// DynamicCompleteFunc Caller type for dynamic completion
type DynamicCompleteFunc func(string) ([]string, []string)

type PrefixCompleterInterface interface {
	Print(prefix string, level int, buf *bytes.Buffer)
	Do(line []rune, pos int) (newLine, commentLine [][]rune, length int)
	GetName() []rune
	GetComment() []rune
	GetChildren() []PrefixCompleterInterface
	SetChildren(children []PrefixCompleterInterface)
}

type DynamicPrefixCompleterInterface interface {
	PrefixCompleterInterface
	IsDynamic() bool
	GetDynamicNames(line []rune) ([][]rune, [][]rune)
}

type PrefixCompleter struct {
	Name            []rune
	Comment         []rune
	Dynamic         bool
	DynamicComments [][]rune
	Callback        DynamicCompleteFunc
	Children        []PrefixCompleterInterface
}

func (p *PrefixCompleter) Tree(prefix string) string {
	buf := bytes.NewBuffer(nil)
	p.Print(prefix, 0, buf)
	return buf.String()
}

func Print(p PrefixCompleterInterface, prefix string, level int, buf *bytes.Buffer) {
	if strings.TrimSpace(string(p.GetName())) != "" {
		buf.WriteString(prefix)
		if level > 0 {
			buf.WriteString("├")
			buf.WriteString(strings.Repeat("─", (level*4)-2))
			buf.WriteString(" ")
		}
		buf.WriteString(string(p.GetName()) + "\n")
		level++
	}
	for _, ch := range p.GetChildren() {
		ch.Print(prefix, level, buf)
	}
}

func (p *PrefixCompleter) Print(prefix string, level int, buf *bytes.Buffer) {
	Print(p, prefix, level, buf)
}

func (p *PrefixCompleter) IsDynamic() bool {
	return p.Dynamic
}

func (p *PrefixCompleter) GetName() []rune {
	return p.Name
}
func (p *PrefixCompleter) GetComment() []rune {
	return p.Comment
}

func (p *PrefixCompleter) GetDynamicNames(line []rune) (names, comments [][]rune) {
	names1, comments1 := p.Callback(string(line))
	for _, name := range names1 {
		names = append(names, []rune(name+" "))
	}
	for _, comment := range comments1 {
		comments = append(comments, []rune(comment))
	}
	return names, comments
}

func (p *PrefixCompleter) GetChildren() []PrefixCompleterInterface {
	return p.Children
}

func (p *PrefixCompleter) SetChildren(children []PrefixCompleterInterface) {
	p.Children = children
}

func NewPrefixCompleter(pc ...PrefixCompleterInterface) *PrefixCompleter {
	return PcItem("", "", pc...)
}

func PcItem(name string, comment string, pc ...PrefixCompleterInterface) *PrefixCompleter {
	name += " "
	return &PrefixCompleter{
		Name:     []rune(name),
		Comment:  []rune(comment),
		Dynamic:  false,
		Children: pc,
	}
}

func PcItemDynamic(callback DynamicCompleteFunc, pc ...PrefixCompleterInterface) *PrefixCompleter {
	return &PrefixCompleter{
		Callback: callback,
		Dynamic:  true,
		Children: pc,
	}
}

func (p *PrefixCompleter) Do(line []rune, pos int) (newLine, commentLine [][]rune, offset int) {
	return doInternal(p, line, pos, line)
}

func Do(p PrefixCompleterInterface, line []rune, pos int) (newLine, commentLine [][]rune, offset int) {
	return doInternal(p, line, pos, line)
}

func doInternal(p PrefixCompleterInterface, line []rune, pos int, origLine []rune) (newLine, commentLine [][]rune, offset int) {
	line = runes.TrimSpaceLeft(line[:pos])
	goNext := false
	var lineCompleter PrefixCompleterInterface
	for _, child := range p.GetChildren() {
		childNames := make([][]rune, 1)
		commentNames := make([][]rune, 1)

		childDynamic, ok := child.(DynamicPrefixCompleterInterface)
		if ok && childDynamic.IsDynamic() {
			childNames, commentNames = childDynamic.GetDynamicNames(origLine)
		} else {
			childNames[0] = child.GetName()
			commentNames[0] = child.GetComment()
		}

		for i, childName := range childNames {
			if len(line) >= len(childName) {
				if runes.HasPrefix(line, childName) {
					if len(line) == len(childName) {
						newLine = append(newLine, []rune{' '})
					} else {
						newLine = append(newLine, childName)
					}
					offset = len(childName)
					lineCompleter = child
					goNext = true
				}
			} else {
				if runes.HasPrefix(childName, line) {
					newLine = append(newLine, childName[len(line):])
					commentLine = append(commentLine, commentNames[i])
					offset = len(line)
					lineCompleter = child
				}
			}
		}
	}

	if len(newLine) != 1 {
		return
	}

	tmpLine := make([]rune, 0, len(line))
	for i := offset; i < len(line); i++ {
		if line[i] == ' ' {
			continue
		}

		tmpLine = append(tmpLine, line[i:]...)
		return doInternal(lineCompleter, tmpLine, len(tmpLine), origLine)
	}

	if goNext {
		return doInternal(lineCompleter, nil, 0, origLine)
	}
	return
}
