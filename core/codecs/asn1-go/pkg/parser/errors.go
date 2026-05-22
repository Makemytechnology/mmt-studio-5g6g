// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package parser

import "fmt"

type Error struct {
	Line    int
	Column  int
	Message string
}

func (e Error) Error() string {
	return fmt.Sprintf("parse %d:%d: %s", e.Line, e.Column, e.Message)
}

type ErrorList []Error

func (el ErrorList) Error() string {
	if len(el) == 0 {
		return "no errors"
	}
	if len(el) == 1 {
		return el[0].Error()
	}
	return fmt.Sprintf("%s (and %d more)", el[0].Error(), len(el)-1)
}
