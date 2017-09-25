// Copyright © 2017 Circonus, Inc. <support@circonus.com>
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//

package reverse

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"math"
	"math/big"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"github.com/circonus-labs/circonus-agent/internal/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// Connection defines a reverse connection
type Connection struct {
	agentAddress  string
	checkCID      string
	cmdCh         chan noitCommand
	commTimeout   time.Duration
	conn          *tls.Conn
	connAttempts  int
	ctx           context.Context
	delay         time.Duration
	dialerTimeout time.Duration
	enabled       bool
	errCh         chan error
	logger        zerolog.Logger
	maxDelay      time.Duration
	metricTimeout time.Duration
	reverseURL    *url.URL
	tlsConfig     *tls.Config
}

type noitHeader struct {
	channelID  uint16
	isCommand  bool
	payloadLen uint32
}

type noitPacket struct {
	header  *noitHeader
	payload []byte
}

type noitCommand struct {
	channelID uint16
	command   string
	request   []byte
}

const (
	// NOTE: TBD, make some of these user-configurable
	commTimeoutSeconds   = 65    // seconds, when communicating with noit
	dialerTimeoutSeconds = 15    // seconds, establishing connection
	metricTimeoutSeconds = 50    // seconds, when communicating with agent
	maxPayloadLen        = 65529 // max unsigned short - 6 (for header)
	maxConnRetry         = 10    // max times to retry a persistently failing connection
	configRetryLimit     = 5     // if failed attempts > threshold, force reconfig
	maxDelaySeconds      = 60    // maximum amount of delay between attempts
	minDelayStep         = 1     // minimum seconds to add on retry
	maxDelayStep         = 20    // maximum seconds to add on retry
)

func init() {
	n, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		rand.Seed(time.Now().UTC().UnixNano())
		return
	}
	rand.Seed(n.Int64())
}

// New creates a new connection
func New(ctx context.Context) (*Connection, error) {
	c := Connection{
		checkCID:      viper.GetString(config.KeyReverseCID),
		cmdCh:         make(chan noitCommand),
		commTimeout:   commTimeoutSeconds * time.Second,
		connAttempts:  0,
		ctx:           ctx,
		delay:         1 * time.Second,
		dialerTimeout: dialerTimeoutSeconds * time.Second,
		enabled:       viper.GetBool(config.KeyReverse),
		errCh:         make(chan error),
		logger:        log.With().Str("pkg", "reverse").Logger(),
		maxDelay:      maxDelaySeconds * time.Second,
		metricTimeout: metricTimeoutSeconds * time.Second,
	}

	if c.enabled {
		c.agentAddress = strings.Replace(viper.GetString(config.KeyListen), "0.0.0.0", "localhost", -1)
		err := c.setCheckConfig()
		if err != nil {
			return nil, err
		}
	}

	return &c, nil
}

// Start reverse connection to the broker
func (c *Connection) Start() error {
	if !c.enabled {
		c.logger.Info().Msg("Reverse disabled, not starting")
		return nil
	}

	c.logger.Info().
		Str("check_bundle", viper.GetString(config.KeyReverseCID)).
		Str("rev_host", c.reverseURL.Hostname()).
		Str("rev_port", c.reverseURL.Port()).
		Str("rev_path", c.reverseURL.Path).
		Str("agent", c.agentAddress).
		Msg("Reverse configuration")

	go func() {
		for { // allow for restarts
			err := c.connectWithRetry()
			if err != nil {
				c.errCh <- err
				break
			}

			err = c.processCommands()
			if err != nil {
				c.logger.Error().Err(err).Msg("connection")
			}

			// shutting down
			if c.isShuttingDown() {
				c.errCh <- nil
				break
			}
		}
	}()

	// block until an error is recieved or some other component exits
	return <-c.errCh
}

// Stop the reverse connection
func (c *Connection) Stop() {
	if !c.enabled {
		return
	}
	c.logger.Debug().Msg("Stopping reverse connection")
	if c.conn == nil {
		return
	}
	err := c.conn.Close()
	if err != nil {
		c.logger.Warn().Err(err).Msg("Closing reverse connection")
	}
}

func (c *Connection) isShuttingDown() bool {
	select {
	case <-c.ctx.Done():
		return true
	default:
		return false
	}
}
