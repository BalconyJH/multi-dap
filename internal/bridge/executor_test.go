package bridge

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestExecutorSerializesOperations(t *testing.T) {
	executor := NewExecutor(nil)
	defer executor.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	secondRan := make(chan struct{}, 1)
	_, firstDone, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		close(started)
		<-release
		return "first", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_, secondDone, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		secondRan <- struct{}{}
		return "second", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondRan:
		t.Fatal("second operation ran while first operation was blocked")
	default:
	}
	close(release)
	if completion := <-firstDone; completion.Result != "first" || completion.Generation != 1 {
		t.Fatalf("first completion = %#v", completion)
	}
	if completion := <-secondDone; completion.Result != "second" || completion.Generation != 1 {
		t.Fatalf("second completion = %#v", completion)
	}
}

func TestExecutorReplaceFencesLateCompletion(t *testing.T) {
	executor := NewExecutor(nil)
	defer executor.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	operation, done, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		close(started)
		<-release
		return "late", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if generation := executor.Replace(nil); generation != 2 {
		t.Fatalf("Replace() generation = %d, want 2", generation)
	}
	close(release)
	completion := <-done
	if completion.Generation != 1 || completion.Operation != operation || completion.Result != "late" {
		t.Fatalf("late completion = %#v, want old generation and operation", completion)
	}
}

func TestExecutorCloseFencesLateCompletion(t *testing.T) {
	executor := NewExecutor(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	_, done, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		close(started)
		<-release
		return nil, errors.New("late failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if generation := executor.Close(); generation != 2 {
		t.Fatalf("Close() generation = %d, want 2", generation)
	}
	if _, _, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) { return nil, nil }); !errors.Is(err, ErrExecutorClosed) {
		t.Fatalf("Submit after Close error = %v, want ErrExecutorClosed", err)
	}
	close(release)
	select {
	case completion := <-done:
		if completion.Generation != 1 || completion.Err == nil {
			t.Fatalf("late close completion = %#v", completion)
		}
	case <-time.After(time.Second):
		t.Fatal("late completion was dropped after Close")
	}
}

func TestExecutorRejectsReplacementAfterClose(t *testing.T) {
	executor := NewExecutor(nil)
	if generation := executor.Close(); generation != 2 {
		t.Fatalf("Close() generation = %d, want 2", generation)
	}
	clientConn, peerConn := net.Pipe()
	defer peerConn.Close()
	replacement := &Client{conn: clientConn}
	if generation := executor.Replace(replacement); generation != 2 {
		t.Fatalf("Replace() generation = %d, want terminal generation 2", generation)
	}
	if !replacement.Poisoned() {
		t.Fatal("replacement client was not closed after executor shutdown")
	}
}

func TestExecutorCloseDrainsAcceptedJobsAndStopsWorker(t *testing.T) {
	executor := NewExecutor(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	_, first, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		close(started)
		<-release
		return "first", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_, second, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		return "second", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	executor.Close()
	close(release)
	if completion := <-first; completion.Result != "first" {
		t.Fatalf("first completion = %#v", completion)
	}
	if completion := <-second; completion.Result != "second" {
		t.Fatalf("second completion = %#v", completion)
	}
	select {
	case <-executor.workerDone:
	case <-time.After(time.Second):
		t.Fatal("executor worker did not exit after draining accepted jobs")
	}
}

func TestExecutorCloseDoesNotWaitForBlockedOperation(t *testing.T) {
	executor := NewExecutor(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	_, done, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
		close(started)
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan struct{})
	go func() {
		executor.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close waited for a blocked operation")
	}
	close(release)
	<-done
	select {
	case <-executor.workerDone:
	case <-time.After(time.Second):
		t.Fatal("executor worker did not exit")
	}
}

func TestExecutorConcurrentSubmitAndCloseDrainsEveryAcceptedJob(t *testing.T) {
	const submitters = 64
	executor := NewExecutor(nil)
	start := make(chan struct{})
	type result struct {
		done <-chan Completion
		err  error
	}
	results := make(chan result, submitters)
	var callers sync.WaitGroup
	for range submitters {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			_, done, err := executor.Submit(context.Background(), func(context.Context, *Client) (any, error) {
				return nil, nil
			})
			results <- result{done: done, err: err}
		}()
	}
	close(start)
	executor.Close()
	callers.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			if !errors.Is(result.err, ErrExecutorClosed) {
				t.Fatalf("Submit() error = %v, want ErrExecutorClosed", result.err)
			}
			continue
		}
		select {
		case completion, ok := <-result.done:
			if !ok || completion.Operation == 0 {
				t.Fatalf("accepted completion = %#v, channel open=%v", completion, ok)
			}
			if _, stillOpen := <-result.done; stillOpen {
				t.Fatal("accepted submission produced more than one completion")
			}
		case <-time.After(time.Second):
			t.Fatal("accepted submission did not complete after Close")
		}
	}
	select {
	case <-executor.workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after concurrent Submit/Close")
	}
}
