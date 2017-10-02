// Copyright © 2017 Circonus, Inc. <support@circonus.com>
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//

package receiver

// Metrics holds metrics received via HTTP PUT/POST
import (
	"sync"

	"github.com/rs/zerolog/log"
)

// Metrics recived via PUT/POST
type Metrics map[string]interface{}

var (
	metricsmu sync.Mutex
	metrics   *Metrics
	logger    = log.With().Str("pkg", "receiver").Logger()
)