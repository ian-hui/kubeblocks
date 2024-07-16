/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package service

import (
	"context"
	"errors"

	"github.com/apecloud/kubeblocks/pkg/kbagent/proto"
)

type Service interface {
	Kind() string
	Version() string
	URI() string

	Start() error

	Decode([]byte) (interface{}, error)

	Call(ctx context.Context, req interface{}) ([]byte, error)

	// Refresh([]proto.Action, []proto.Probe) error
}

func New(actions []proto.Action, probes []proto.Probe) ([]Service, error) {
	sa, err := newActionService(actions)
	if err != nil {
		return nil, err
	}
	sp, err := newProbeService(sa, probes)
	if err != nil {
		return nil, err
	}
	return []Service{sa, sp}, nil
}

var ErrNotImplemented = errors.New("NotImplemented")
