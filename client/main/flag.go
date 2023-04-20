package main

import (
	"fmt"
	"strings"
)

type zeroFlags []bool

func (i *zeroFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *zeroFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}

type closeFlags []bool

func (i *closeFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *closeFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}
