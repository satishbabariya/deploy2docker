/*
MIT License

Copyright (c) 2020 Satish Babariya

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh"
)

func main() {
	var address string
	var username string
	var key string

	app := &cli.App{
		Name:  "Deploy To Docker",
		Usage: "Deploy to Remote Docker using SSH",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "address",
				Usage:       "host address with port",
				Required:    true,
				Destination: &address,
			},
			&cli.StringFlag{
				Name:        "user",
				Usage:       "host server user",
				Required:    true,
				Destination: &username,
			},
			&cli.StringFlag{
				Name:        "key",
				Usage:       "host server ssh key",
				Required:    true,
				Destination: &key,
			},
		},

		Action: func(c *cli.Context) error {
			fmt.Println("root action")

			client, err := ConnectWithKey(address, username, key)
			if err != nil {
				log.Fatal(err)
				return err
			}
			defer client.Close()

			// Establish connection with docker
			remote, err := client.Dial("unix", "/var/run/docker.sock")
			if err != nil {
				log.Fatalln(err)
			}

			const SockAddr = "/tmp/docker.sock"
			if err := os.RemoveAll(SockAddr); err != nil {
				log.Fatal(err)
			}

			os.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")

			// Start local server to forward traffic to remote connection
			local, err := net.Listen("unix", SockAddr)
			if err != nil {
				log.Fatalln(err)
				return err
			}
			defer local.Close()

			fmt.Println(local)

			// Handle incoming connections
			for {
				client, err := local.Accept()
				if err != nil {
					log.Fatalln(err)
					return err
				}
				fmt.Println(client)
				handleClient(client, remote)
			}

			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func ConnectWithKey(addr, user, key string) (*ssh.Client, error) {
	//signer, err := ssh.ParsePrivateKeyWithPassphrase(key, []byte("password"))
	signer, err := ssh.ParsePrivateKey([]byte(key))
	if err != nil {
		log.Fatalf("unable to parse private key: %v", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.HostKeyCallback(func(hostname string, remote net.Addr, key ssh.PublicKey) error { return nil }),
	}

	conn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func Connect(addr, user, password string) (*ssh.Client, error) {
	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		Timeout:         30 * time.Second,
		HostKeyCallback: ssh.HostKeyCallback(func(hostname string, remote net.Addr, key ssh.PublicKey) error { return nil }),
	}

	conn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func handleClient(client net.Conn, remote net.Conn) {
	defer client.Close()
	chDone := make(chan bool)

	// Start remote -> local data transfer
	go func() {
		_, err := io.Copy(client, remote)
		if err != nil {
			log.Println("error while copy remote->local:", err)
		}
		chDone <- true
	}()

	// Start local -> remote data transfer
	go func() {
		_, err := io.Copy(remote, client)
		if err != nil {
			log.Println(err)
		}
		chDone <- true
	}()

	<-chDone
}
