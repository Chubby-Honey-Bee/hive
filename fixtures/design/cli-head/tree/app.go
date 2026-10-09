package main

import "io"

// run is the program: args are the arguments after the command name, and
// the three streams are its standard input, output and error. It returns
// the exit code. Not yet implemented.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return 2
}
