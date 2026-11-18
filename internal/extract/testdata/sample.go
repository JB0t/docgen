// Package sample is a test fixture.
package sample

import "os"

// Greeter says hello.
type Greeter struct{}

// Greet returns a greeting.
func (g *Greeter) Greet(name string) string { return "hi " + name + os.Getenv("GREETING_SUFFIX") }

func helper() {}
