// Package sftpx is the file-transfer half of an SSH session.
//
// The terminal path (internal/sshx) can move bytes through trzsz or ZMODEM, but
// both need software on the far side (trzsz, lrzsz). SFTP is the one file
// protocol every sshd speaks out of the box, which makes it the fallback for a
// machine where nothing was installed on purpose.
//
// The package is a small wrapper over github.com/pkg/sftp on purpose: the
// protocol is not ours to reimplement, and every method here is one the file
// panel calls directly. Splitting it out of internal/sshx keeps the terminal
// path free of file-browser concerns.
package sftpx

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// chunkSize bounds one read/write leg. Small enough that progress looks alive
// for big files, large enough that a 1 GB upload is not 4000 progress events.
const chunkSize = 256 * 1024

// Entry is one row of a remote directory listing.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

// Listing is a directory and what is in it. Path is resolved (absolute, or
// joined onto the remote working directory when the request was relative), so
// the panel can show where it actually is.
type Listing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

// Client is one SFTP subsystem channel on an existing SSH connection.
type Client struct {
	c *sftp.Client
}

// Open starts the sftp subsystem on conn. The SSH connection already carries a
// shell; SFTP is another channel on it, so no password, key or second factor is
// asked for again.
func Open(conn *ssh.Client) (*Client, error) {
	c, err := sftp.NewClient(conn)
	if err != nil {
		return nil, fmt.Errorf("open sftp subsystem: %w", err)
	}
	return &Client{c: c}, nil
}

// Close releases the subsystem channel. The SSH connection itself is owned by
// the session and is not closed here.
func (c *Client) Close() error { return c.c.Close() }

// Home reports the remote user's working directory, which is where the file
// panel starts.
func (c *Client) Home() (string, error) {
	wd, err := c.c.Getwd()
	if err != nil {
		return "", err
	}
	return wirePath(wd), nil
}

// wirePath is the form SFTP puts on the wire: forward slashes, and a Windows
// drive path written the way win32 OpenSSH expects it ("/C:/Users/x").
// Everything the panel sends back has already been produced by a listing, so
// this is a no-op for a Linux server; it matters when the far side is Windows
// too (or when the tests run there).
func wirePath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	if len(p) >= 2 && p[1] == ':' && (len(p) == 2 || p[2] == '/') {
		return "/" + p
	}
	return p
}

// List enumerates dir. An empty dir means the remote working directory.
// Directories sort first, then names, so the panel needs no second pass.
func (c *Client) List(dir string) (Listing, error) {
	if dir == "" {
		home, err := c.c.Getwd()
		if err != nil {
			return Listing{}, fmt.Errorf("remote working directory: %w", err)
		}
		dir = home
	}
	dir = wirePath(dir)
	resolved := dir
	if !path.IsAbs(resolved) {
		if wd, err := c.c.Getwd(); err == nil {
			resolved = path.Join(wirePath(wd), resolved)
		}
	}

	infos, err := c.c.ReadDir(dir)
	if err != nil {
		return Listing{}, fmt.Errorf("list %s: %w", dir, err)
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, Entry{
			Name:    info.Name(),
			Path:    path.Join(resolved, info.Name()),
			Dir:     info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
	return Listing{Path: resolved, Entries: entries}, nil
}

// Mkdir creates one directory.
func (c *Client) Mkdir(dir string) error {
	if err := c.c.Mkdir(wirePath(dir)); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

// Remove deletes a file, or an empty directory when dir is true. A directory
// delete that silently recursed would be a very expensive mis-click.
func (c *Client) Remove(p string, dir bool) error {
	wire := wirePath(p)
	var err error
	if dir {
		err = c.c.RemoveDirectory(wire)
	} else {
		err = c.c.Remove(wire)
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

// Rename moves or renames one entry.
func (c *Client) Rename(oldPath, newPath string) error {
	if err := c.c.Rename(wirePath(oldPath), wirePath(newPath)); err != nil {
		return fmt.Errorf("rename %s to %s: %w", oldPath, newPath, err)
	}
	return nil
}

// Upload copies one local file to remotePath, creating or truncating it.
// progress is called with bytes done and the file's size; it may be nil.
func (c *Client) Upload(localPath, remotePath string, progress func(done, total int64)) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}
	dst, err := c.c.Create(wirePath(remotePath))
	if err != nil {
		return fmt.Errorf("create %s: %w", remotePath, err)
	}

	if err := copyWithProgress(dst, src, info.Size(), progress); err != nil {
		_ = dst.Close()
		return fmt.Errorf("upload %s: %w", filepath.Base(localPath), err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("finish %s: %w", remotePath, err)
	}
	return nil
}

// Download copies remotePath into localPath, creating parent directories.
// progress is called with bytes done and the remote file's size; it may be nil.
func (c *Client) Download(remotePath, localPath string, progress func(done, total int64)) error {
	src, err := c.c.Open(wirePath(remotePath))
	if err != nil {
		return fmt.Errorf("open %s: %w", remotePath, err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", remotePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", localPath, err)
	}
	dst, err := os.OpenFile(localPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", localPath, err)
	}

	if err := copyWithProgress(dst, src, info.Size(), progress); err != nil {
		_ = dst.Close()
		return fmt.Errorf("download %s: %w", path.Base(remotePath), err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("finish %s: %w", localPath, err)
	}
	return nil
}

func copyWithProgress(dst io.Writer, src io.Reader, total int64, progress func(done, total int64)) error {
	buf := make([]byte, chunkSize)
	var done int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
