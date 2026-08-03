package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

// refresh queries enabled providers concurrently, but commits quota-change
// detection only after every provider in the logical round has returned.
func (s *AppService) refresh(ctx context.Context) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	cfg, configVersion := s.configSnapshotWithVersion()
	roundID := s.startRefreshRound()
	ids := enabledProviderIDs(cfg)
	if len(ids) > 0 {
		s.app.Event.Emit("usage:loading", usageLoadingEvent{
			ConfigVersion: configVersion,
			RoundID:       roundID,
			ProviderIDs:   ids,
		})
	}

	resultsCh := make(chan providers.Result, len(ids))
	var wg sync.WaitGroup
	for i := range cfg.Providers {
		pc := cfg.Providers[i]
		if !pc.Enabled {
			continue
		}
		wg.Add(1)
		go func(pc config.ProviderConfig) {
			defer wg.Done()
			res := queryProvider(ctx, pc)
			if res == nil {
				return
			}
			s.checkAlerts(res, cfg)
			s.mu.Lock()
			s.lastStatus = upsertResult(s.lastStatus, *res)
			s.mu.Unlock()
			s.app.Event.Emit("usage:update", usageUpdateEvent{
				ConfigVersion: configVersion,
				RoundID:       roundID,
				Results:       []providers.Result{*res},
			})
			resultsCh <- *res
		}(pc)
	}
	wg.Wait()
	close(resultsCh)

	results := make([]providers.Result, 0, len(ids))
	for res := range resultsCh {
		results = append(results, res)
	}
	s.commitRefreshRound(roundID, configVersion, cfg, results)
}

func queryProvider(ctx context.Context, pc config.ProviderConfig) *providers.Result {
	p, err := providers.New(pc)
	if err != nil {
		return &providers.Result{
			ProviderID:   pc.ID,
			ProviderName: pc.Name,
			Error:        err.Error(),
			UpdatedAt:    time.Now().Format("15:04:05"),
		}
	}
	res, err := p.Query(ctx)
	if err != nil {
		log.Printf("query %s: %v", pc.ID, err)
		return &providers.Result{
			ProviderID:   pc.ID,
			ProviderName: p.Name(),
			Error:        err.Error(),
			UpdatedAt:    time.Now().Format("15:04:05"),
		}
	}
	if res == nil {
		return &providers.Result{
			ProviderID:   pc.ID,
			ProviderName: p.Name(),
			Error:        fmt.Sprintf("provider %s returned no result", pc.ID),
			UpdatedAt:    time.Now().Format("15:04:05"),
		}
	}
	return res
}

func (s *AppService) startRefreshRound() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRound++
	return s.refreshRound
}

func (s *AppService) commitRefreshRound(roundID, configVersion uint64, cfg *config.AppConfig, results []providers.Result) {
	event := s.commitQuotaRound(roundID, configVersion, cfg, results)
	s.app.Event.Emit("usage:complete", event)
}

func (s *AppService) commitQuotaRound(roundID, configVersion uint64, cfg *config.AppConfig, results []providers.Result) usageCompleteEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaSnapshots == nil {
		s.quotaSnapshots = map[string]quotaSnapshot{}
	}
	if s.lastChangedRound == nil {
		s.lastChangedRound = map[string]uint64{}
	}

	changed := make(map[string]struct{})
	for i := range results {
		res := results[i]
		if res.Error != "" {
			continue
		}
		snapshot := quotaSnapshotOf(res)
		previous, seen := s.quotaSnapshots[res.ProviderID]
		if seen && !quotaSnapshotsEqual(previous, snapshot) && dynamicSortEnabled(cfg, res.ProviderID) {
			s.lastChangedRound[res.ProviderID] = roundID
			changed[res.ProviderID] = struct{}{}
		}
		s.quotaSnapshots[res.ProviderID] = snapshot
	}
	s.lastStatus = sortResultsByRecentChange(s.lastStatus, s.lastChangedRound, cfg.Providers)
	order := make([]string, 0, len(s.lastStatus))
	for i := range s.lastStatus {
		order = append(order, s.lastStatus[i].ProviderID)
	}
	changedIDs := make([]string, 0, len(changed))
	for _, id := range order {
		if _, ok := changed[id]; ok {
			changedIDs = append(changedIDs, id)
		}
	}

	event := usageCompleteEvent{
		ConfigVersion:      configVersion,
		RoundID:            roundID,
		ChangedProviderIDs: changedIDs,
		ProviderIDs:        order,
	}
	return event
}
