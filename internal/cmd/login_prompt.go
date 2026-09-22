package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

func defaultProjectPrompt() func(string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	return func(prompt string) (string, error) {
		fmt.Print(prompt)
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			if errRead == io.EOF {
				return strings.TrimSpace(line), nil
			}
			return "", errRead
		}
		return strings.TrimSpace(line), nil
	}
}
