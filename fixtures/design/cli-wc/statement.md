# Implement a word-count tool

Package `main` (files `main.go`, `app.go`) runs `run(args []string, stdin io.Reader, stdout, stderr io.Writer) int`. Implement `run` as a `wc`-like tool. The signature stays; `main.go` stays as it is.

Usage: `tool [-l] [-w] [-c] [file ...]`

- `-l` counts lines (the number of newline characters), `-w` words (runs of non-blank characters, as `strings.Fields` splits), `-c` bytes. With no flag all three are shown. Whichever flags are given, the counts shown appear in the order lines, words, bytes.
- One output line per input: the counts separated by single spaces, then, when the input is a named file, a space and the file name, then a newline. With no file the input is standard input and no name is printed.
- With two or more files a last line gives the sums, named `total`.
- A file that cannot be opened prints `tool: NAME: ERROR` (the error's text) to standard error, is skipped, and makes the exit code 1; the other files are still counted and the total still printed.
- An argument that starts with `-` and is not one of the three flags prints a usage line to standard error and exits 2 at once. Flags come before file names.
- Otherwise the exit code is 0.
