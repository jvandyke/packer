package ssh

import (
	"bytes"
	"code.google.com/p/go.crypto/ssh"
	"fmt"
	"github.com/mitchellh/packer/packer"
	"io"
	"log"
	"net"
	"path/filepath"
)

type comm struct {
	client *ssh.ClientConn
}

// Creates a new packer.Communicator implementation over SSH. This takes
// an already existing TCP connection and SSH configuration.
func New(c net.Conn, config *ssh.ClientConfig) (result *comm, err error) {
	client, err := ssh.Client(c, config)
	result = &comm{client}
	return
}

func (c *comm) Start(cmd *packer.RemoteCmd) (err error) {
	session, err := c.client.NewSession()
	if err != nil {
		return
	}

	// Setup our session
	session.Stdin = cmd.Stdin
	session.Stdout = cmd.Stdout
	session.Stderr = cmd.Stderr

	// Request a PTY
	termModes := ssh.TerminalModes{
		ssh.ECHO:          0,     // do not echo
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err = session.RequestPty("xterm", 80, 40, termModes); err != nil {
		return
	}

	log.Printf("starting remote command: %s", cmd.Command)
	err = session.Start(cmd.Command + "\n")
	if err != nil {
		return
	}

	// Start a goroutine to wait for the session to end and set the
	// exit boolean and status.
	go func() {
		defer session.Close()

		err := session.Wait()
		cmd.ExitStatus = 0
		if err != nil {
			exitErr, ok := err.(*ssh.ExitError)
			if ok {
				cmd.ExitStatus = exitErr.ExitStatus()
			}
		}

		cmd.Exited = true
	}()

	return
}

func (c *comm) Upload(path string, input io.Reader) error {
	log.Println("Opening new SSH session")
	session, err := c.client.NewSession()
	if err != nil {
		return err
	}

	defer session.Close()

	// Get a pipe to stdin so that we can send data down
	w, err := session.StdinPipe()
	if err != nil {
		return err
	}

	// Set stderr/stdout to a bytes buffer
	stderr := new(bytes.Buffer)
	stdout := new(bytes.Buffer)
	session.Stderr = stderr
	session.Stdout = stdout

	// We only want to close once, so we nil w after we close it,
	// and only close in the defer if it hasn't been closed already.
	defer func() {
		if w != nil {
			w.Close()
		}
	}()

	// The target directory and file for talking the SCP protocol
	target_dir := filepath.Dir(path)
	target_file := filepath.Base(path)

	// Start the sink mode on the other side
	// TODO(mitchellh): There are probably issues with shell escaping the path
	log.Println("Starting remote scp process in sink mode")
	if err = session.Start("scp -vt " + target_dir); err != nil {
		return err
	}

	// Determine the length of the upload content by copying it
	// into an in-memory buffer. Note that this means what we upload
	// must fit into memory.
	log.Println("Copying input data into in-memory buffer so we can get the length")
	input_memory := new(bytes.Buffer)
	if _, err = io.Copy(input_memory, input); err != nil {
		return err
	}

	// Start the protocol
	log.Println("Beginning file upload...")
	fmt.Fprintln(w, "C0644", input_memory.Len(), target_file)
	io.Copy(w, input_memory)
	fmt.Fprint(w, "\x00")

	// TODO(mitchellh): Each step above results in a 0/1/2 being sent by
	// the remote side to confirm. We should check for those confirmations.

	// Close the stdin, which sends an EOF, and then set w to nil so that
	// our defer func doesn't close it again since that is unsafe with
	// the Go SSH package.
	log.Println("Upload complete, closing stdin pipe")
	w.Close()
	w = nil

	// Wait for the SCP connection to close, meaning it has consumed all
	// our data and has completed. Or has errored.
	log.Println("Waiting for SSH session to complete")
	err = session.Wait()
	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			// Otherwise, we have an ExitErorr, meaning we can just read
			// the exit status
			log.Printf("non-zero exit status: %d", exitErr.ExitStatus())
		}

		return err
	}

	log.Printf("scp stdout (length %d): %#v", stdout.Len(), stdout.Bytes())
	log.Printf("scp stderr (length %d): %s", stderr.Len(), stderr.String())

	return nil
}

func (c *comm) Download(string, io.Writer) error {
	panic("not implemented yet")
}
