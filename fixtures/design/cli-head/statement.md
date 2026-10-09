# Implement a head tool

Package `main` (files `main.go`, `app.go`) runs `run(args []string, stdin io.Reader, stdout, stderr io.Writer) int`. Implement `run` as a `head`-like tool. The signature stays; `main.go` stays as it is.

Usage: `tool [-n N] [-c N] [file ...]`

- With no flag the first 10 lines of each input are printed. `-n N` prints the first N lines, `-c N` the first N bytes. A line ends at a newline, which is printed with it; a final line without a newline counts as a line and is printed as it is. `N` is a non-negative integer; anything else, or both flags together, prints a usage line to standard error and exits 2 at once. Flags come before file names.
- With no file the input is standard input and nothing but the content is printed.
- With two or more files each is preceded by a header line `==> NAME <==`, and a blank line separates one file's output from the next header. With one file there is no header.
- A file that cannot be opened prints `tool: NAME: ERROR` (the error's text) to standard error, is skipped, and makes the exit code 1; the other files are still printed.
- Otherwise the exit code is 0.
