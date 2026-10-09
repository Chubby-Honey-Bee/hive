package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage: tool [-n N] [-c N] [file ...]")
	return 2
}

// head writes the first n lines, or the first n bytes when byBytes, of r.
func head(w io.Writer, r io.Reader, n int, byBytes bool) error {
	if byBytes {
		_, err := io.Copy(w, io.LimitReader(r, int64(n)))
		return err
	}
	br := bufio.NewReader(r)
	for i := 0; i < n; i++ {
		line, err := br.ReadString('\n')
		if line != "" {
			if _, werr := io.WriteString(w, line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// run is a head-like tool: tool [-n N] [-c N] [file ...].
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	n, byBytes, seen := 10, false, false
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		if (args[i] != "-n" && args[i] != "-c") || seen || i+1 >= len(args) {
			return usage(stderr)
		}
		v, err := strconv.Atoi(args[i+1])
		if err != nil || v < 0 {
			return usage(stderr)
		}
		n, byBytes, seen = v, args[i] == "-c", true
		i++
	}
	files := args[i:]
	if len(files) == 0 {
		if err := head(stdout, stdin, n, byBytes); err != nil {
			fmt.Fprintf(stderr, "tool: stdin: %v\n", err)
			return 1
		}
		return 0
	}
	exit := 0
	printed := 0
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintf(stderr, "tool: %s: %v\n", name, err)
			exit = 1
			continue
		}
		if len(files) > 1 {
			if printed > 0 {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintf(stdout, "==> %s <==\n", name)
		}
		printed++
		err = head(stdout, f, n, byBytes)
		f.Close()
		if err != nil {
			fmt.Fprintf(stderr, "tool: %s: %v\n", name, err)
			exit = 1
		}
	}
	return exit
}
