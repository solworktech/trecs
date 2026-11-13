// `trecs` records a terminal session, plays it back, and uploads it to a Trecs server. It runs the shell on a pseudo-terminal
// and writes what happens as a recording (terminal.jsonl, and optioanlly audio), marking each prompt, command and output;
// it can also stream a session live (trecs livestream) and scan a recording for secrets before it leaves the machine.
// See the man page or README for the sub-commands.
package main
