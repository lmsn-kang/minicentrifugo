package worker

import (
	"log"
	"sync"
)

type Task func()

type Pool struct {
	tasks chan Task
	wg    sync.WaitGroup
	quit  chan struct{}
}

func New(size, queueSize int) *Pool {
	p := &Pool{
		tasks: make(chan Task, queueSize),
		quit:  make(chan struct{}),
	}

	for i := 0; i < size; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}

	log.Printf("[WorkerPool] Started with %d workers, queue size: %d", size, queueSize)
	return p
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()

	for {
		select {
		case task, ok := <-p.tasks:
			if !ok {
				return
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[WorkerPool] Worker %d panic recovered: %v", id, r)
					}
				}()
				task()
			}()

		case <-p.quit:
			return
		}
	}
}

func (p *Pool) Submit(task Task) bool {
	select {
	case p.tasks <- task:
		return true
	default:
		return false
	}
}

func (p *Pool) SubmitMust(task Task) {
	p.tasks <- task
}

func (p *Pool) Close() {
	log.Println("[WorkerPool] Closing...")
	close(p.quit)
	close(p.tasks)
	p.wg.Wait()
	log.Println("[WorkerPool] Closed")
}

func (p *Pool) Stats() (queueLen int, isClosed bool) {
	return len(p.tasks), p.tasks == nil || p.quit == nil
}
