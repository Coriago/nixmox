package task

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

var (
	_ engine.LocalStore    = (*Journal)(nil)
	_ engine.LogStore      = (*Journal)(nil)
	_ engine.ProgressStore = (*Journal)(nil)
)

type Journal struct {
	mu   sync.Mutex
	root *os.Root
	lock *os.File
}

type admissionFile struct {
	Record  domain.AdmissionRecord `json:"record"`
	Initial domain.Task            `json:"initial_task"`
}

func OpenJournal(directory string) (*Journal, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		root.Close()
		return nil, errors.New("execution journal already owned by another daemon")
	}
	j := &Journal{root: root, lock: lock}
	for _, path := range []string{"receipts", "jobs"} {
		if err = root.MkdirAll(path, 0700); err != nil {
			j.Shutdown()
			return nil, err
		}
	}
	return j, nil
}

func (j *Journal) Shutdown() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return errors.Join(j.lock.Close(), j.root.Close())
}

func TaskID(command domain.ID) domain.ID { return shared.ID("task_", command) }

func receiptPath(id domain.ID) string { return "receipts/" + shared.Filename(id) + ".json" }

func taskPath(id domain.ID) string { return "jobs/" + shared.Filename(id) + "/task.json" }

func (j *Journal) read(path string, v any) error {
	f, e := j.root.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(v)
}

func (j *Journal) syncDir(path string) error {
	d, e := j.root.Open(path)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func (j *Journal) write(path string, value any) error {
	dir := filepath.Dir(path)
	if err := j.root.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// The journal's process lock and mutex serialize this fixed staging filename.
	temp := path + ".new"
	f, err := j.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = j.root.Rename(temp, path); err != nil {
		return err
	}
	if err = j.syncDir(dir); err != nil {
		return err
	}
	return j.syncDir(filepath.Dir(dir))
}

func (j *Journal) admission(id domain.ID) (admissionFile, error) {
	var file admissionFile
	err := j.read(receiptPath(id), &file)
	return file, err
}

func (j *Journal) ensureTask(file admissionFile) error {
	if file.Record.Receipt.Admission == "rejected" {
		return nil
	}
	var task domain.Task
	err := j.read(taskPath(file.Initial.ID), &task)
	if errors.Is(err, os.ErrNotExist) {
		return j.write(taskPath(file.Initial.ID), file.Initial)
	}
	return err
}

func (j *Journal) Admit(ctx context.Context, record domain.AdmissionRecord, task domain.Task) (domain.AdmissionRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if record.Envelope.Command.ID == "" || record.Envelope.RequestHash == "" || task.ID != TaskID(record.Envelope.Command.ID) {
		return record, errors.New("invalid admission identity")
	}
	prior, err := j.admission(record.Envelope.Command.ID)
	if err == nil {
		if prior.Record.Envelope.RequestHash != record.Envelope.RequestHash {
			return record, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already has a different request"}
		}
		return prior.Record, j.ensureTask(prior)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	file := admissionFile{record, task}
	// Initial task is in the same atomic record as the receipt. A crash before the
	// separate mutable task projection is written reconstructs this exact task.
	if err = j.write(receiptPath(record.Envelope.Command.ID), file); err != nil {
		return record, err
	}
	return record, j.ensureTask(file)
}

// Reject persists a terminal admission outcome without inventing an execution.
func (j *Journal) Reject(ctx context.Context, e domain.Envelope, detail *domain.Error) (domain.Receipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	receipt := domain.Receipt{CommandID: e.Command.ID, Admission: "rejected"}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if prior, err := j.admission(e.Command.ID); err == nil {
		if prior.Record.Envelope.RequestHash != e.RequestHash {
			return receipt, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used"}
		}
		return prior.Record.Receipt, prior.Record.Error
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	record := domain.AdmissionRecord{SchemaVersion: 1, Envelope: e, Receipt: receipt, Error: detail}
	if err := j.write(receiptPath(e.Command.ID), admissionFile{Record: record}); err != nil {
		return receipt, err
	}
	return receipt, detail
}

func (j *Journal) Receipt(ctx context.Context, id domain.ID) (domain.AdmissionRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.AdmissionRecord{}, err
	}
	r, e := j.admission(id)
	return r.Record, e
}

func (j *Journal) Task(ctx context.Context, id domain.ID) (domain.Task, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var task domain.Task
	if err := ctx.Err(); err != nil {
		return task, err
	}
	err := j.read(taskPath(id), &task)
	return task, err
}

func (j *Journal) Progress(ctx context.Context, task domain.ID) (domain.TaskProgress, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result domain.TaskProgress
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := j.read("jobs/"+shared.Filename(task)+"/progress.json", &result)
	return result, err
}
func (j *Journal) Activity(ctx context.Context, task, activity domain.ID) (domain.Activity, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result domain.Activity
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := j.read("jobs/"+shared.Filename(task)+"/activities/"+shared.Filename(activity)+".json", &result)
	return result, err
}
func (j *Journal) SaveTask(ctx context.Context, t domain.Task, expected domain.Version) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var old domain.Task
	if err := j.read(taskPath(t.ID), &old); err != nil {
		return err
	}
	if old.Version != expected || t.Version != expected+1 {
		return &domain.Error{Code: "VERSION_CONFLICT", Message: "Task changed"}
	}
	if terminal(old.State) {
		return errors.New("terminal task is immutable")
	}
	if old.CommandID != t.CommandID || old.OwnerNodeID != t.OwnerNodeID || old.Scope != t.Scope {
		return errors.New("task identity is immutable")
	}
	if old.State != t.State && !allowedTransition(old.State, t.State) {
		return fmt.Errorf("invalid task transition %s -> %s", old.State, t.State)
	}
	return j.write(taskPath(t.ID), t)
}

func (j *Journal) Recoverable(ctx context.Context, page domain.PageRequest) (domain.Page[domain.Task], error) {
	return j.listTasks(ctx, page, true)
}

// Tasks lists retained tasks newest first. Cursors identify the last returned
// task; execution recovery uses its separate ID-ordered scan.
func (j *Journal) Tasks(ctx context.Context, page domain.PageRequest) (domain.Page[domain.Task], error) {
	return j.listTasks(ctx, page, false)
}

func (j *Journal) listTasks(ctx context.Context, page domain.PageRequest, recovery bool) (domain.Page[domain.Task], error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := domain.Page[domain.Task]{Items: []domain.Task{}}
	directory, err := j.root.Open("receipts")
	if err != nil {
		return result, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var admission admissionFile
		if err = j.read("receipts/"+entry.Name(), &admission); err != nil {
			return result, err
		}
		if admission.Record.Receipt.Admission == "rejected" {
			continue
		}
		if err = j.ensureTask(admission); err != nil {
			return result, err
		}
		var task domain.Task
		if err = j.read(taskPath(admission.Initial.ID), &task); err != nil {
			return result, err
		}
		if !recovery || (!terminal(task.State) && string(task.ID) > page.Cursor) {
			result.Items = append(result.Items, task)
		}
	}
	sort.Slice(result.Items, func(i, k int) bool {
		a, b := result.Items[i], result.Items[k]
		if recovery {
			return a.ID < b.ID
		}
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if !recovery && page.Cursor != "" {
		position := -1
		for i, item := range result.Items {
			if string(item.ID) == page.Cursor {
				position = i
				break
			}
		}
		if position < 0 {
			return domain.Page[domain.Task]{}, &domain.Error{Code: "CURSOR_EXPIRED", Message: "Task cursor unavailable"}
		}
		result.Items = result.Items[position+1:]
	}
	limit := page.Limit
	if limit == 0 {
		limit = domain.PageDefault
	}
	if limit < 1 || limit > domain.PageLimit {
		return result, errors.New("invalid recovery page limit")
	}
	if len(result.Items) > limit {
		cursor := string(result.Items[limit-1].ID)
		result.NextCursor = &cursor
		result.Items = result.Items[:limit]
	}
	return result, nil
}

func logPath(task domain.ID, activity *domain.ID) string {
	name := "task"
	if activity != nil {
		name = "activity-" + shared.Filename(*activity)
	}
	return "jobs/" + shared.Filename(task) + "/" + name + ".log"
}

func (j *Journal) Append(ctx context.Context, task domain.ID, activity *domain.ID, data []byte) (domain.LogEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	event := domain.LogEvent{TaskID: task, ActivityID: activity}
	if err := ctx.Err(); err != nil {
		return event, err
	}
	if len(data) > domain.LogEventLimit/2 {
		return event, errors.New("split log chunks before persistence")
	}
	var t domain.Task
	if err := j.read(taskPath(task), &t); err != nil {
		return event, err
	}
	event.OwnerNodeID = t.OwnerNodeID
	path := logPath(task, activity)
	if _, err := j.root.Stat(path + ".closed"); err == nil {
		return event, errors.New("log is closed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return event, err
	}
	f, err := j.root.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return event, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return event, err
	}
	event.Offset = info.Size()
	if _, err = f.Write(data); err != nil {
		return event, err
	}
	if err = f.Sync(); err != nil {
		return event, err
	}
	if err = j.syncDir(filepath.Dir(path)); err != nil {
		return event, err
	}
	event.DataBase64 = base64.StdEncoding.EncodeToString(data)
	return event, nil
}

func (j *Journal) Read(ctx context.Context, task domain.ID, activity *domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	chunk := domain.LogChunk{Offset: offset, NextOffset: offset}
	if err := ctx.Err(); err != nil {
		return chunk, err
	}
	if offset < 0 || limit < 1 || limit > domain.LogReadLimit {
		return chunk, errors.New("invalid log bounds")
	}
	var t domain.Task
	if err := j.read(taskPath(task), &t); err != nil {
		return chunk, err
	}
	path := logPath(task, activity)
	f, err := j.root.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return chunk, err
	}
	size := int64(0)
	if f != nil {
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return chunk, e
		}
		size = info.Size()
	}
	if offset > size {
		return chunk, &domain.Error{Code: "RANGE_NOT_SATISFIABLE", Message: "Offset is after the log end"}
	}
	if f != nil {
		data := make([]byte, min(int64(limit), size-offset))
		n, e := f.ReadAt(data, offset)
		if e != nil && e != io.EOF {
			return chunk, e
		}
		chunk.DataBase64 = base64.StdEncoding.EncodeToString(data[:n])
		chunk.NextOffset += int64(n)
	}
	_, err = j.root.Stat(path + ".closed")
	chunk.Complete = err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return chunk, err
	}
	return chunk, nil
}

func (j *Journal) Close(ctx context.Context, task domain.ID, activity *domain.ID) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return j.write(logPath(task, activity)+".closed", true)
}

type snapshotReader struct {
	io.Reader
	io.Closer
}

func (j *Journal) Snapshot(ctx context.Context, task domain.ID, activity *domain.ID) (domain.Download, io.ReadCloser, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Download{}, nil, err
	}
	var t domain.Task
	if err := j.read(taskPath(task), &t); err != nil {
		return domain.Download{}, nil, err
	}
	f, err := j.root.Open(logPath(task, activity))
	if err != nil {
		return domain.Download{}, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return domain.Download{}, nil, err
	}
	return domain.Download{OwnerNodeID: t.OwnerNodeID, Filename: string(task) + ".log", ContentType: "text/plain", Bytes: info.Size()}, snapshotReader{io.LimitReader(f, info.Size()), f}, nil
}

func terminal(s domain.TaskState) bool {
	return s == domain.Succeeded || s == domain.Failed || s == domain.Cancelled
}

func allowedTransition(from, to domain.TaskState) bool {
	switch from {
	case domain.Queued:
		return to == domain.Running || to == domain.Cancelling
	case domain.Running:
		return to == domain.Succeeded || to == domain.Failed || to == domain.Blocked || to == domain.Reconciling || to == domain.Cancelling
	case domain.Blocked:
		return to == domain.Running || to == domain.Cancelling || to == domain.Reconciling
	case domain.Reconciling:
		return to == domain.Running || to == domain.Succeeded || to == domain.Failed || to == domain.Blocked
	case domain.Cancelling:
		return to == domain.Cancelled || to == domain.Reconciling
	}
	return false
}
