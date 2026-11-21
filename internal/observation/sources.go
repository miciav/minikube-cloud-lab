// Package observation reads aggregate measurements inside the lab's existing service boundaries.
package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Sample is a measured gauge for one course resource.
type Sample struct {
	Metric, Resource string
	Value            float64
}
type command func(...string) ([]byte, error)

// Read selects just the running service belonging to this lab's managed-service project.
func Read(ctx context.Context, source string) ([]Sample, error) {
	if source != "queue" && source != "object-storage" {
		return nil, errors.New("unknown source")
	}
	runtime, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	defer func() { _ = runtime.Close() }()
	rows, err := runtime.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", "com.docker.compose.project="+scope()+"-"+source),
		filters.Arg("label", "com.docker.compose.service="+source))})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, errors.New("source unavailable")
	}
	cmd := func(args ...string) ([]byte, error) {
		task, e := runtime.ContainerExecCreate(ctx, rows[0].ID, container.ExecOptions{AttachStdout: true, Cmd: args})
		if e != nil {
			return nil, e
		}
		stream, e := runtime.ContainerExecAttach(ctx, task.ID, container.ExecAttachOptions{})
		if e != nil {
			return nil, e
		}
		defer stream.Close()
		stop := context.AfterFunc(ctx, stream.Close)
		defer stop()
		var buf bytes.Buffer
		const maxOutput = 1 << 20
		_, e = stdcopy.StdCopy(&buf, io.Discard, io.LimitReader(stream.Reader, maxOutput+1))
		if e != nil {
			return nil, e
		}
		if buf.Len() >= maxOutput {
			return nil, errors.New("source response too large")
		}
		status, e := runtime.ContainerExecInspect(ctx, task.ID)
		if e != nil {
			return nil, e
		}
		if status.Running || status.ExitCode != 0 {
			return nil, errors.New("source command did not finish successfully")
		}
		return buf.Bytes(), nil
	}
	if source == "queue" {
		return queues(cmd)
	}
	return buckets(cmd)
}

func buckets(cmd command) ([]Sample, error) {
	type bucket struct {
		ID            string
		GlobalAliases []string
	}
	var list []bucket
	data, e := cmd("/garage", "json-api", "ListBuckets")
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(data, &list); e != nil {
		return nil, e
	}
	if len(list) > 128 {
		return nil, errors.New("bucket limit exceeded")
	}
	out := []Sample{}
	for _, b := range list {
		args, _ := json.Marshal(map[string]string{"id": b.ID})
		data, e := cmd("/garage", "json-api", "GetBucketInfo", string(args))
		if e != nil {
			return nil, e
		}
		var info struct{ Objects, Bytes *int64 }
		if e = json.Unmarshal(data, &info); e != nil {
			return nil, e
		}
		if info.Objects == nil || info.Bytes == nil {
			return nil, errors.New("missing bucket counters")
		}
		name := b.ID
		if len(b.GlobalAliases) > 0 {
			name = b.GlobalAliases[0]
		}
		out = append(out, Sample{"bucket_objects", name, float64(*info.Objects)}, Sample{"bucket_bytes", name, float64(*info.Bytes)})
	}
	return out, nil
}

var queuePath = regexp.MustCompile(`^/[a-zA-Z0-9_-]+/[a-zA-Z0-9_.-]+$`)

func queues(cmd command) ([]Sample, error) {
	const base = "http://127.0.0.1:9324"
	get := func(path string) ([]byte, error) {
		return cmd("curl", "--max-time", "3", "--fail", "--silent", base+path)
	}
	data, e := get("/?Action=ListQueues&MaxResults=129")
	if e != nil {
		return nil, e
	}
	var list struct {
		XMLName xml.Name `xml:"ListQueuesResponse"`
		URLs    []string `xml:"ListQueuesResult>QueueUrl"`
		Next    string   `xml:"ListQueuesResult>NextToken"`
	}
	if e = xml.Unmarshal(data, &list); e != nil {
		return nil, e
	}
	if len(list.URLs) > 128 || list.Next != "" {
		return nil, errors.New("queue limit exceeded")
	}
	attributes := map[string]string{"ApproximateNumberOfMessages": "queue_visible", "ApproximateNumberOfMessagesNotVisible": "queue_inflight", "ApproximateNumberOfMessagesDelayed": "queue_delayed"}
	out := []Sample{}
	for _, raw := range list.URLs {
		u, e := url.Parse(raw)
		if e != nil || !queuePath.MatchString(u.Path) {
			return nil, errors.New("invalid queue path")
		}
		data, e := get(u.Path + "?Action=GetQueueAttributes&AttributeName.1=All")
		if e != nil {
			return nil, e
		}
		var info struct {
			Attr []struct{ Name, Value string } `xml:"GetQueueAttributesResult>Attribute"`
		}
		if e = xml.Unmarshal(data, &info); e != nil {
			return nil, e
		}
		seen := map[string]bool{}
		for _, a := range info.Attr {
			metric, ok := attributes[a.Name]
			if !ok {
				continue
			}
			n, e := strconv.ParseUint(a.Value, 10, 64)
			if e != nil {
				return nil, e
			}
			out = append(out, Sample{metric, u.Path[1:], float64(n)})
			seen[metric] = true
		}
		if len(seen) != len(attributes) {
			return nil, fmt.Errorf("incomplete queue counters")
		}
	}
	return out, nil
}

func scope() string {
	if v := os.Getenv("CLOUD_COURSE_PROJECT"); v != "" {
		return v
	}
	return "mcl"
}

// Installed checks labeled containers, including a stopped collector awaiting recovery.
func Installed(ctx context.Context) (bool, error) {
	runtime, e := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if e != nil {
		return false, e
	}
	defer func() { _ = runtime.Close() }()
	rows, e := runtime.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "io.course.monitoring.scope="+scope()), filters.Arg("label", "com.docker.compose.service=monitoring"))})
	return len(rows) > 0, e
}
