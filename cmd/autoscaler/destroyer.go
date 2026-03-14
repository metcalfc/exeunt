package main

import (
	"context"
)

func (p *Provisioner) Destroy(ctx context.Context, event WorkflowJobEvent) {
	jobID := event.WorkflowJob.ID
	log := p.logger.With("job_id", jobID)

	record, ok := p.tracker.Get(jobID)
	if !ok {
		log.Debug("job not tracked, ignoring completed event")
		return
	}

	vmName := record.VMName
	log = log.With("vm", vmName)
	log.Info("destroying VM")

	p.tracker.Update(jobID, StatusDestroying)

	if err := p.ssh.RemoveVM(ctx, vmName); err != nil {
		log.Error("failed to destroy VM", "error", err)
		// Don't remove from tracker — reaper will catch it
		return
	}

	p.tracker.Remove(jobID)
	<-p.semaphore
	log.Info("VM destroyed")
}
