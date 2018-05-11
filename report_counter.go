package main

import (
	"fmt"
	"os"
	"strconv"
	"github.com/jimdn/gomonitor"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s id value\n", os.Args[0])
		return
	}
	id, err := strconv.ParseInt(os.Args[1], 10, 64)
	if err != nil {
		fmt.Println("id param error\n")
		return
	}
	value, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		fmt.Println("value param error\n")
		return
	}
	gomonitor.Add(int(id), value)
}
