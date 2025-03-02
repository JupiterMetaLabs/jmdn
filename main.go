package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"

	"gossipnode/node"
)

// Main function with simple CLI
func main() {
	n, err := node.NewNode()
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()

	fmt.Println("Commands: 'msg <peer_multiaddr> <message>' or 'file <peer_multiaddr> <filepath>' or 'exit'")

	var wg sync.WaitGroup
	wg.Add(1)

	// Command-line input loop
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "exit" {
				return
			}

			parts := strings.SplitN(input, " ", 3)
			if len(parts) < 2 {
				fmt.Println("Invalid command")
				continue
			}

			switch parts[0] {
			case "msg":
				if len(parts) != 3 {
					fmt.Println("Usage: msg <peer_multiaddr> <message>")
					continue
				}
				err := node.SendMessage(n, parts[1], parts[2])
				if err != nil {
					fmt.Println("Error:", err)
				}
			case "file":
				if len(parts) != 3 {
					fmt.Println("Usage: file <peer_multiaddr> <filepath>")
					continue
				}
				err := node.SendFile(n, parts[1], parts[2])
				if err != nil {
					fmt.Println("Error:", err)
				}
			default:
				fmt.Println("Unknown command")
			}
		}
	}()

	wg.Wait()
}