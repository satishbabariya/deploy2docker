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
	"context"
	"deploy2docker/internal"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/moby/term"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

func main() {

	app := &cli.App{
		Name:  "Deploy To Docker",
		Usage: "Deploy to Remote Docker using SSH",
		Action: func(c *cli.Context) error {
			sshclient := internal.NewSSHClient("ec2-3-22-224-209.us-east-2.compute.amazonaws.com:22", "ec2-user")
			client, err := sshclient.ConnectWithKey("/Users/satishbabariya/Desktop/deployit.pem")
			if err != nil {
				logrus.Errorln(fmt.Errorf("error while connecting to remote: %v", err))
				return err
			}
			defer client.Close()

			// Establish connection with docker
			remote, err := client.Dial("unix", "/var/run/docker.sock")
			if err != nil {
				logrus.Errorln(fmt.Errorf("error while connecting to remote docker: %v", err))
				return err
			}
			defer remote.Close()

			logrus.Infoln("Connected to remote docker")

			const SocketAddress = "/tmp/docker.sock"
			if _, err := os.Stat(SocketAddress); err == nil {
				if err := os.RemoveAll(SocketAddress); err != nil {
					return err
				}
			}

			os.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")

			// Start local server to forward traffic to remote connection
			local, err := net.Listen("unix", SocketAddress)
			if err != nil {
				logrus.Errorln(fmt.Errorf("error while listening to local unix socks: %v", err))
				return err
			}
			defer local.Close()

			// Handle incoming connections
			go func() {
				for {
					client, err := local.Accept()
					if err != nil {
						logrus.Fatalln(err)
						return
					}
					handleClient(client, remote)
				}
			}()

			cli, err := docker.NewClientWithOpts(docker.FromEnv)
			if err != nil {
				return err
			}

			err = BuildAndDeploy(c.Context, cli)
			if err != nil {
				return err
			}

			logrus.Infoln("Deployed to remote docker")
			err = RunContainer(c.Context, cli)
			if err != nil {
				return err
			}

			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		logrus.Fatal(err)
	}
}

func handleClient(client net.Conn, remote net.Conn) {
	// defer client.Close()
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

func RunContainer(ctx context.Context, cli *docker.Client) error {

	// stop old container if exists and remove it
	err := cli.ContainerRemove(ctx, "go-echo-example", types.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		logrus.Errorln(fmt.Errorf("error while removing old container: %v", err))
		return err
	}

	// Create a new container
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "satishbabariya/go-echo-example:latest",
		ExposedPorts: nat.PortSet{
			"8080/tcp": struct{}{},
		},
		AttachStdout: true,
	}, &container.HostConfig{
		PortBindings: nat.PortMap{
			"8080/tcp": []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: "8080",
				},
			},
		},
	}, &network.NetworkingConfig{}, nil, "go-echo-example")
	if err != nil {
		return err
	}

	// Start the container
	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	// Wait for the container to finish
	// statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	// select {
	// case err := <-errCh:
	// 	if err != nil {
	// 		return err
	// 	}
	// case <-statusCh:
	// }

	// Inspect the container
	// inspect, err := cli.ContainerInspect(ctx, resp.ID)
	// if err != nil {
	// 	return err
	// }

	// // Print the container's IP address
	// fmt.Println(inspect)

	// get container logs
	out, err := cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return err
	}

	io.Copy(os.Stdout, out)
	defer out.Close()

	return nil
}

func BuildAndDeploy(ctx context.Context, cli *docker.Client) error {
	tar, err := archive.TarWithOptions("/Users/satishbabariya/Desktop/go-echo-example", &archive.TarOptions{})
	if err != nil {
		logrus.Errorln(fmt.Errorf("error while creating tar: %v", err))
		return err
	}

	defer tar.Close()

	// build docker image
	resp, err := cli.ImageBuild(ctx, tar, types.ImageBuildOptions{
		Tags:   []string{"satishbabariya/go-echo-example:latest"},
		Remove: true,
	})
	if err != nil {
		logrus.Errorln(fmt.Errorf("error while building image: %v", err))
		return err
	}

	// _, err = io.Copy(logrus.New().Out, resp.Body)
	// if err != nil {
	// 	logrus.Errorln(fmt.Errorf("error while copying build output: %v", err))
	// 	return err
	// }

	termFd, isTerm := term.GetFdInfo(os.Stderr)
	jsonmessage.DisplayJSONMessagesStream(resp.Body, os.Stderr, termFd, isTerm, nil)

	defer resp.Body.Close()

	logrus.Infoln("Built docker image satishbabariya/go-echo-example")

	return nil
}

func LogContainer(ctx context.Context, cli *docker.Client, resp container.ContainerCreateCreatedBody) error {
	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-statusCh:
	}

	out, err := cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return err
	}

	stdcopy.StdCopy(os.Stdout, os.Stderr, out)
	return nil
}
