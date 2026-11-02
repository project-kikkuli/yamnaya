package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/spf13/cobra"
)

func workAuthorizeCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "work-authorize <assignment>", Hidden: true, Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		send := json.NewEncoder(cmd.OutOrStdout())
		err := e.core.AuthorizeWork(args[0], func(w core.RemoteAssignment) error {
			if err := send.Encode(core.WorkPermit{Work: w}); err != nil {
				return err
			}
			var result core.WorkPermit
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&result); err != nil {
				return err
			}
			if result.Error != "" {
				if result.Final {
					return core.Final(errors.New(result.Error))
				}
				return errors.New(result.Error)
			}
			return nil
		})
		result := core.WorkPermit{}
		if err != nil {
			result.Error = err.Error()
			result.Final = errors.Is(err, core.ErrFinal)
		}
		return send.Encode(result)
	})}
}

func (r *remoteHosts) StartWork(from, host string, w core.RemoteAssignment) error {
	reply, err := r.Sync(host, core.RelaySync{From: from, As: host, Start: &w})
	if err != nil {
		return err
	}
	if reply.StartError != "" {
		if reply.StartFinal {
			return core.Final(errors.New(reply.StartError))
		}
		return errors.New(reply.StartError)
	}
	return nil
}

func (r *remoteHosts) AuthorizeWork(origin, id string, run func(core.RemoteAssignment) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.ssh, append(sshArgs(origin, "-o", "BatchMode=yes"), remoteCommand([]string{"work-authorize", id}))...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	decoder := json.NewDecoder(out)
	var permit core.WorkPermit
	if err = decoder.Decode(&permit); err != nil {
		in.Close()
		return errors.Join(fmt.Errorf("live authority %s: %w", origin, core.ErrUnreachable), err, cmd.Wait())
	}
	var runErr error
	if permit.Work.Assignment.ID != "" {
		runErr = run(permit.Work)
		result := core.WorkPermit{}
		if runErr != nil {
			result.Error = runErr.Error()
			result.Final = errors.Is(runErr, core.ErrFinal)
		}
		if err = json.NewEncoder(in).Encode(result); err != nil {
			runErr = errors.Join(runErr, err)
		}
		in.Close()
		if err = decoder.Decode(&permit); err != nil {
			runErr = errors.Join(runErr, err)
		}
	} else {
		in.Close()
	}
	waitErr := cmd.Wait()
	if permit.Error == core.ErrWorkRevoked.Error() {
		return core.ErrWorkRevoked
	}
	if permit.Error != "" {
		runErr = errors.Join(runErr, errors.New(permit.Error))
	}
	if waitErr != nil {
		return errors.Join(runErr, sshErr(waitErr, stderr.Bytes()))
	}
	return runErr
}
