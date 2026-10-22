package retention

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const journalTimeout = 30 * time.Second
const maxIntentBytes = 64 << 20

// FileJournal is a single-process synthetic/standalone journal. Production
// composition should use SQLite under shared root ownership instead. Directory
// must already be privately owned, outside the snapshot root. Files are synced
// before atomic rename and the directory is synced after it. A sync error is an
// error, not a durability claim. Calls are serialized within this instance.
type FileJournal struct {
	directory string
	mu        sync.Mutex
}

func NewFileJournal(directory string) (*FileJournal, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("journal requires private directory")
	}
	return &FileJournal{directory: directory}, nil
}
func journalName(id string) (string, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", errors.New("invalid intent ID")
	}
	return id + ".json", nil
}
func (j *FileJournal) Load(ctx context.Context, id string) (Intent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Intent{}, err
	}
	name, err := journalName(id)
	if err != nil {
		return Intent{}, err
	}
	root, err := os.OpenRoot(j.directory)
	if err != nil {
		return Intent{}, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return Intent{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxIntentBytes {
		return Intent{}, errors.New("invalid intent file")
	}
	f, err := root.Open(name)
	if err != nil {
		return Intent{}, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, maxIntentBytes+1))
	d.DisallowUnknownFields()
	var in Intent
	if err := d.Decode(&in); err != nil {
		return Intent{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Intent{}, errors.New("trailing intent data")
	}
	return in, nil
}
func (j *FileJournal) Save(ctx context.Context, in Intent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := journalName(in.Preview.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(b) > maxIntentBytes {
		return errors.New("intent too large")
	}
	root, err := os.OpenRoot(j.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	// The fixed staging name is safe under the instance mutex. Cross-process
	// ownership is intentionally the coordinator's responsibility.
	tmp := name + ".pending"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		if e := root.Remove(tmp); e != nil {
			return e
		}
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
