/*
Copyright 2018 BlackRock, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gateways

import (
	"fmt"
	"hash/fnv"

	zlog "github.com/rs/zerolog"
)

// Hasher hashes a string
func Hasher(value string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("%v", h.Sum32())
}

// SetValidateReason set the result of event source validation
func SetValidEventSource(v *ValidEventSource, reason string, valid bool) {
	v.Reason = &reason
	v.IsValid = &valid
}

// HandleEventsFromEventSource handles events from the event source.
func HandleEventsFromEventSource(name *string, eventStream Eventing_StartEventSourceServer, dataCh chan []byte, errorCh chan error, doneCh chan struct{}, log *zlog.Logger) error {
	for {
		select {
		case data := <-dataCh:
			log.Info().Str("event-source-name", *name).Msg("new event received, dispatching to gateway client")
			err := eventStream.Send(&Event{
				Name:    name,
				Payload: data,
			})
			if err != nil {
				return err
			}

		case err := <-errorCh:
			log.Info().Str("event-source-name", *name).Err(err).Msg("error occurred while getting event from event source")
			return err

		case <-eventStream.Context().Done():
			log.Info().Str("event-source-name", *name).Msg("connection is closed by client")
			doneCh <- struct{}{}
			return nil
		}
	}
}
