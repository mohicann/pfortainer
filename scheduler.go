package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// nextRunTime returns the next scheduled time after `from` for the given frequency.
func nextRunTime(frequency string, from time.Time) time.Time {
	switch frequency {
	case "hourly":
		return from.Truncate(time.Hour).Add(time.Hour)
	case "daily":
		next := from.Truncate(24 * time.Hour).Add(24 * time.Hour)
		return next
	case "weekly":
		// next Monday 00:00 UTC
		d := from.Truncate(24 * time.Hour).Add(24 * time.Hour)
		for d.Weekday() != time.Monday {
			d = d.Add(24 * time.Hour)
		}
		return d
	case "monthly":
		y, m, _ := from.Date()
		return time.Date(y, m+1, 1, 0, 0, 0, 0, from.Location())
	default:
		return from.Add(24 * time.Hour)
	}
}

// snapLabel returns a snapshot name based on the prefix and current time.
func snapLabel(prefix string) string {
	return prefix + "-" + time.Now().UTC().Format("20060102-150405")
}

// Scheduler runs periodic tasks (auto-snapshot, scrub) stored in ConfigDB.
type Scheduler struct {
	cdb *ConfigDB
}

func newScheduler(cdb *ConfigDB) *Scheduler {
	return &Scheduler{cdb: cdb}
}

// Start runs the scheduler loop. Call in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.tick() // run immediately on startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	due, err := s.cdb.DueSchedules()
	if err != nil {
		log.Printf("scheduler: DueSchedules: %v", err)
		return
	}
	for _, sched := range due {
		if err := s.run(sched); err != nil {
			log.Printf("scheduler: task %d (%s %s): %v", sched.ID, sched.Type, sched.Target, err)
		}
	}
}

// RunNow executes a schedule immediately regardless of its next_run time.
func (s *Scheduler) RunNow(id int64) error {
	sched, err := s.cdb.GetSchedule(id)
	if err != nil {
		return fmt.Errorf("schedule %d not found: %w", id, err)
	}
	return s.run(*sched)
}

func (s *Scheduler) run(sched DBSchedule) error {
	now := time.Now()
	next := nextRunTime(sched.Frequency, now)

	var runErr error
	switch sched.Type {
	case "snapshot":
		runErr = s.runSnapshot(sched)
	case "scrub":
		runErr = s.runScrub(sched)
	default:
		runErr = fmt.Errorf("unknown schedule type: %s", sched.Type)
	}

	if err := s.cdb.UpdateScheduleRun(sched.ID, now, next); err != nil {
		log.Printf("scheduler: UpdateScheduleRun %d: %v", sched.ID, err)
	}
	return runErr
}

func (s *Scheduler) runSnapshot(sched DBSchedule) error {
	name := snapLabel(sched.Prefix)
	log.Printf("[scheduler] snapshot %s@%s", sched.Target, name)
	if err := createSnapshot(sched.Target, name); err != nil {
		return err
	}
	if sched.Retention > 0 {
		if err := pruneSnapshots(sched.Target, sched.Prefix, sched.Retention); err != nil {
			log.Printf("[scheduler] prune %s prefix=%s: %v", sched.Target, sched.Prefix, err)
		}
	}
	return nil
}

func (s *Scheduler) runScrub(sched DBSchedule) error {
	log.Printf("[scheduler] scrub %s", sched.Target)
	out, err := exec.Command("zpool", "scrub", sched.Target).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("zpool scrub %s: %s", sched.Target, msg)
	}
	return nil
}
