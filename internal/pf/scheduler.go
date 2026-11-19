package pf

import (
	"fmt"
	"log"
	"time"
)

// StartScheduler: 스케줄(60s)·알림(5m)·복제(10m) 루프. 시작하자마자 스케줄 틱 1회.
func StartScheduler(hc *HostClient, db *ConfigDB) {
	if schedulerDisabled() {
		return
	}
	s, al, rp := schedIntervals()
	go func() {
		st := time.NewTicker(time.Duration(s) * time.Second)
		at := time.NewTicker(time.Duration(al) * time.Second)
		rt := time.NewTicker(time.Duration(rp) * time.Second)
		schedTick(hc, db)
		for {
			select {
			case <-st.C:
				schedTick(hc, db)
			case <-at.C:
				checkAndFireAlerts(db, hc)
			case <-rt.C:
				replTick(hc, db)
			}
		}
	}()
}

func schedTick(hc *HostClient, db *ConfigDB) {
	due, err := db.DueSchedules()
	if err != nil {
		log.Printf("scheduler: due_schedules: %v", err)
		return
	}
	for i := range due {
		if err := runSchedule(hc, db, &due[i]); err != nil {
			log.Printf("scheduler: task %d (%s %s): %v", due[i].ID, due[i].Kind, due[i].Target, err)
		}
	}
}

func replTick(hc *HostClient, db *ConfigDB) {
	due, err := db.DueReplTasks()
	if err != nil {
		log.Printf("scheduler: due_repl_tasks: %v", err)
		return
	}
	for _, t := range due {
		res, err := runReplication(hc, t.SourceDataset, t.TargetPath, t.LastSnapshot, t.Recursive)
		if err != nil {
			db.UpdateReplTaskResult(t.ID, t.LastSnapshot, "error", err.Error())
			continue
		}
		db.UpdateReplTaskResult(t.ID, res.CurrentSnapshot, "ok", "")
		if t.Retention > 0 {
			if err := pruneSnapshots(hc, t.TargetPath, "auto", int32(t.Retention)); err != nil {
				log.Printf("[scheduler] prune target %s : %v", t.TargetPath, err)
			}
		}
	}
}

func snapLabel(prefix string) string {
	n := time.Now().UTC()
	return fmt.Sprintf("%s-%04d%02d%02d-%02d%02d%02d", prefix, n.Year(), int(n.Month()), n.Day(), n.Hour(), n.Minute(), n.Second())
}

// runSchedule: 한 건 실행 후 last_run/next_run 갱신(실패해도 갱신).
func runSchedule(hc *HostClient, db *ConfigDB, s *DbSchedule) error {
	now := time.Now()
	next := NextRunTime(s.Frequency, now)
	var err error
	switch s.Kind {
	case "snapshot":
		name := snapLabel(s.Prefix)
		if err = createSnapshot(hc, s.Target, name); err == nil && s.Retention > 0 {
			if perr := pruneSnapshots(hc, s.Target, s.Prefix, int32(s.Retention)); perr != nil {
				log.Printf("[scheduler] prune %s prefix=%s: %v", s.Target, s.Prefix, perr)
			}
		}
	case "scrub":
		r := runOutput("zpool", []string{"scrub", s.Target}, nil)
		switch {
		case r.spawnErr != "":
			err = fmt.Errorf("%s", r.spawnErr)
		case !r.ok:
			msg := trimSpace(string(r.stderr))
			if msg == "" {
				msg = r.status
			}
			err = fmt.Errorf("zpool scrub %s: %s", s.Target, msg)
		}
	case "smart_test":
		tt := s.Prefix
		if tt == "" || tt == "auto" {
			tt = "short"
		}
		_, err = startSmartTest(hc, s.Target, tt)
	default:
		err = fmt.Errorf("unknown schedule type: %s", s.Kind)
	}
	if uerr := db.UpdateScheduleRun(s.ID, now, next); uerr != nil {
		log.Printf("scheduler: update_schedule_run %d: %v", s.ID, uerr)
	}
	return err
}
