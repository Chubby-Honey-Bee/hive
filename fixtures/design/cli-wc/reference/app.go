package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

type counts struct{ lines, words, bytes int }

func (c counts) add(o counts) counts {
	return counts{c.lines + o.lines, c.words + o.words, c.bytes + o.bytes}
}

func count(r io.Reader) (counts, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return counts{}, err
	}
	return counts{lines: bytes.Count(data, []byte("\n")), words: len(strings.Fields(string(data))), bytes: len(data)}, nil
}

// run is a wc-like tool: tool [-l] [-w] [-c] [file ...].
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var showL, showW, showC bool
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "-l":
			showL = true
		case "-w":
			showW = true
		case "-c":
			showC = true
		default:
			fmt.Fprintln(stderr, "usage: tool [-l] [-w] [-c] [file ...]")
			return 2
		}
	}
	if !showL && !showW && !showC {
		showL, showW, showC = true, true, true
	}
	files := args[i:]
	print := func(c counts, name string) {
		var cols []string
		if showL {
			cols = append(cols, fmt.Sprint(c.lines))
		}
		if showW {
			cols = append(cols, fmt.Sprint(c.words))
		}
		if showC {
			cols = append(cols, fmt.Sprint(c.bytes))
		}
		line := strings.Join(cols, " ")
		if name != "" {
			line += " " + name
		}
		fmt.Fprintln(stdout, line)
	}
	if len(files) == 0 {
		c, err := count(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "tool: stdin: %v\n", err)
			return 1
		}
		print(c, "")
		return 0
	}
	exit := 0
	var total counts
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintf(stderr, "tool: %s: %v\n", name, err)
			exit = 1
			continue
		}
		c, err := count(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(stderr, "tool: %s: %v\n", name, err)
			exit = 1
			continue
		}
		total = total.add(c)
		print(c, name)
	}
	if len(files) > 1 {
		print(total, "total")
	}
	return exit
}
