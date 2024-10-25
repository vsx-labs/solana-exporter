package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/asymmetric-research/solana_exporter/pkg/rpc"
	"github.com/asymmetric-research/solana_exporter/pkg/slog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"net/http"
	"time"
)

const (
	SkipStatusLabel = "status"
	StateLabel      = "state"
	NodekeyLabel    = "nodekey"
	VotekeyLabel    = "votekey"
	VersionLabel    = "version"
	AddressLabel    = "address"
	EpochLabel      = "epoch"
	IdentityLabel   = "identity"

	StatusSkipped = "skipped"
	StatusValid   = "valid"

	StateCurrent    = "current"
	StateDelinquent = "delinquent"
)

type SolanaCollector struct {
	rpcClient rpc.Provider
	logger    *zap.SugaredLogger

	// config:
	slotPace         time.Duration
	balanceAddresses []string
	identity         string

	/// descriptors:
	ValidatorActive         *GaugeDesc
	ValidatorActiveStake    *GaugeDesc
	ValidatorLastVote       *GaugeDesc
	ValidatorRootSlot       *GaugeDesc
	ValidatorDelinquent     *GaugeDesc
	AccountBalances         *GaugeDesc
	NodeVersion             *GaugeDesc
	NodeIsHealthy           *GaugeDesc
	NodeNumSlotsBehind      *GaugeDesc
	NodeMinimumLedgerSlot   *GaugeDesc
	NodeFirstAvailableBlock *GaugeDesc
}

func NewSolanaCollector(
	provider rpc.Provider, slotPace time.Duration, balanceAddresses, nodekeys, votekeys []string, identity string,
) *SolanaCollector {
	collector := &SolanaCollector{
		rpcClient:        provider,
		logger:           slog.Get(),
		slotPace:         slotPace,
		balanceAddresses: CombineUnique(balanceAddresses, nodekeys, votekeys),
		identity:         identity,
		ValidatorActive: NewGaugeDesc(
			"solana_validator_active",
			fmt.Sprintf(
				"Total number of active validators, grouped by %s ('%s' or '%s')",
				StateLabel, StateCurrent, StateDelinquent,
			),
			StateLabel,
		),
		ValidatorActiveStake: NewGaugeDesc(
			"solana_validator_active_stake",
			fmt.Sprintf("Active stake per validator (represented by %s and %s)", VotekeyLabel, NodekeyLabel),
			VotekeyLabel, NodekeyLabel,
		),
		ValidatorLastVote: NewGaugeDesc(
			"solana_validator_last_vote",
			fmt.Sprintf("Last voted-on slot per validator (represented by %s and %s)", VotekeyLabel, NodekeyLabel),
			VotekeyLabel, NodekeyLabel,
		),
		ValidatorRootSlot: NewGaugeDesc(
			"solana_validator_root_slot",
			fmt.Sprintf("Root slot per validator (represented by %s and %s)", VotekeyLabel, NodekeyLabel),
			VotekeyLabel, NodekeyLabel,
		),
		ValidatorDelinquent: NewGaugeDesc(
			"solana_validator_delinquent",
			fmt.Sprintf("Whether a validator (represented by %s and %s) is delinquent", VotekeyLabel, NodekeyLabel),
			VotekeyLabel, NodekeyLabel,
		),
		AccountBalances: NewGaugeDesc(
			"solana_account_balance",
			fmt.Sprintf("Solana account balances, grouped by %s", AddressLabel),
			AddressLabel,
		),
		NodeVersion: NewGaugeDesc(
			"solana_node_version",
			"Node version of solana",
			VersionLabel,
		),
		NodeIsHealthy: NewGaugeDesc(
			"solana_node_is_healthy",
			fmt.Sprintf("Whether a node (%s) is healthy", IdentityLabel),
			IdentityLabel,
		),
		NodeNumSlotsBehind: NewGaugeDesc(
			"solana_node_num_slots_behind",
			fmt.Sprintf(
				"The number of slots that the node (%s) is behind the latest cluster confirmed slot.",
				IdentityLabel,
			),
			IdentityLabel,
		),
		NodeMinimumLedgerSlot: NewGaugeDesc(
			"solana_node_minimum_ledger_slot",
			fmt.Sprintf("The lowest slot that the node (%s) has information about in its ledger.", IdentityLabel),
			IdentityLabel,
		),
		NodeFirstAvailableBlock: NewGaugeDesc(
			"solana_node_first_available_block",
			fmt.Sprintf(
				"The slot of the lowest confirmed block that has not been purged from the node's (%s) ledger.",
				IdentityLabel,
			),
			IdentityLabel,
		),
	}
	return collector
}

func (c *SolanaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.ValidatorActive.Desc
	ch <- c.NodeVersion.Desc
	ch <- c.ValidatorActiveStake.Desc
	ch <- c.ValidatorLastVote.Desc
	ch <- c.ValidatorRootSlot.Desc
	ch <- c.ValidatorDelinquent.Desc
	ch <- c.AccountBalances.Desc
	ch <- c.NodeIsHealthy.Desc
	ch <- c.NodeNumSlotsBehind.Desc
	ch <- c.NodeMinimumLedgerSlot.Desc
	ch <- c.NodeFirstAvailableBlock.Desc
}

func (c *SolanaCollector) collectVoteAccounts(ctx context.Context, ch chan<- prometheus.Metric) {
	voteAccounts, err := c.rpcClient.GetVoteAccounts(ctx, rpc.CommitmentConfirmed, nil)
	if err != nil {
		c.logger.Errorf("failed to get vote accounts: %v", err)
		ch <- c.ValidatorActive.NewInvalidMetric(err)
		ch <- c.ValidatorActiveStake.NewInvalidMetric(err)
		ch <- c.ValidatorLastVote.NewInvalidMetric(err)
		ch <- c.ValidatorRootSlot.NewInvalidMetric(err)
		ch <- c.ValidatorDelinquent.NewInvalidMetric(err)
		return
	}

	ch <- c.ValidatorActive.MustNewConstMetric(float64(len(voteAccounts.Delinquent)), StateDelinquent)
	ch <- c.ValidatorActive.MustNewConstMetric(float64(len(voteAccounts.Current)), StateCurrent)

	for _, account := range append(voteAccounts.Current, voteAccounts.Delinquent...) {
		accounts := []string{account.VotePubkey, account.NodePubkey}
		ch <- c.ValidatorActiveStake.MustNewConstMetric(float64(account.ActivatedStake), accounts...)
		ch <- c.ValidatorLastVote.MustNewConstMetric(float64(account.LastVote), accounts...)
		ch <- c.ValidatorRootSlot.MustNewConstMetric(float64(account.RootSlot), accounts...)
	}

	for _, account := range voteAccounts.Current {
		ch <- c.ValidatorDelinquent.MustNewConstMetric(0, account.VotePubkey, account.NodePubkey)
	}
	for _, account := range voteAccounts.Delinquent {
		ch <- c.ValidatorDelinquent.MustNewConstMetric(1, account.VotePubkey, account.NodePubkey)
	}
}

func (c *SolanaCollector) collectVersion(ctx context.Context, ch chan<- prometheus.Metric) {
	version, err := c.rpcClient.GetVersion(ctx)

	if err != nil {
		c.logger.Errorf("failed to get version: %v", err)
		ch <- c.NodeVersion.NewInvalidMetric(err)
		return
	}

	ch <- c.NodeVersion.MustNewConstMetric(1, version)
}
func (c *SolanaCollector) collectMinimumLedgerSlot(ctx context.Context, ch chan<- prometheus.Metric) {
	slot, err := c.rpcClient.GetMinimumLedgerSlot(ctx)

	if err != nil {
		c.logger.Errorf("failed to get minimum lidger slot: %v", err)
		ch <- c.NodeMinimumLedgerSlot.NewInvalidMetric(err)
		return
	}

	ch <- c.NodeMinimumLedgerSlot.MustNewConstMetric(float64(*slot), c.identity)
}
func (c *SolanaCollector) collectFirstAvailableBlock(ctx context.Context, ch chan<- prometheus.Metric) {
	block, err := c.rpcClient.GetFirstAvailableBlock(ctx)

	if err != nil {
		c.logger.Errorf("failed to get first available block: %v", err)
		ch <- c.NodeFirstAvailableBlock.NewInvalidMetric(err)
		return
	}

	ch <- c.NodeFirstAvailableBlock.MustNewConstMetric(float64(*block), c.identity)
}

func (c *SolanaCollector) collectBalances(ctx context.Context, ch chan<- prometheus.Metric) {
	balances, err := FetchBalances(ctx, c.rpcClient, c.balanceAddresses)
	if err != nil {
		c.logger.Errorf("failed to get balances: %v", err)
		ch <- c.AccountBalances.NewInvalidMetric(err)
		return
	}

	for address, balance := range balances {
		ch <- c.AccountBalances.MustNewConstMetric(balance, address)
	}
}

func (c *SolanaCollector) collectHealth(ctx context.Context, ch chan<- prometheus.Metric) {
	var (
		isHealthy      = 1
		numSlotsBehind int64
	)

	_, err := c.rpcClient.GetHealth(ctx)
	if err != nil {
		var rpcError *rpc.RPCError
		if errors.As(err, &rpcError) {
			var errorData rpc.NodeUnhealthyErrorData
			if rpcError.Data == nil {
				// if there is no data, then this is some unexpected error and should just be logged
				c.logger.Errorf("failed to get health: %v", err)
				ch <- c.NodeIsHealthy.NewInvalidMetric(err)
				ch <- c.NodeNumSlotsBehind.NewInvalidMetric(err)
				return
			}
			if err = rpc.UnpackRpcErrorData(rpcError, errorData); err != nil {
				// if we error here, it means we have the incorrect format
				c.logger.Fatalf("failed to unpack %s rpc error: %v", rpcError.Method, err.Error())
			}
			isHealthy = 0
			numSlotsBehind = errorData.NumSlotsBehind
		} else {
			// if it's not an RPC error, log it
			c.logger.Errorf("failed to get health: %v", err)
			ch <- c.NodeIsHealthy.NewInvalidMetric(err)
			ch <- c.NodeNumSlotsBehind.NewInvalidMetric(err)
			return
		}
	}

	ch <- c.NodeIsHealthy.MustNewConstMetric(float64(isHealthy), c.identity)
	ch <- c.NodeNumSlotsBehind.MustNewConstMetric(float64(numSlotsBehind), c.identity)

	return
}

func (c *SolanaCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.collectVoteAccounts(ctx, ch)
	c.collectVersion(ctx, ch)
	c.collectBalances(ctx, ch)
	c.collectHealth(ctx, ch)
	c.collectMinimumLedgerSlot(ctx, ch)
	c.collectFirstAvailableBlock(ctx, ch)
}

func main() {
	logger := slog.Get()
	ctx := context.Background()

	config := NewExporterConfigFromCLI()
	if config.ComprehensiveSlotTracking {
		logger.Warn(
			"Comprehensive slot tracking will lead to potentially thousands of new " +
				"Prometheus metrics being created every epoch.",
		)
	}

	client := rpc.NewRPCClient(config.RpcUrl, config.HttpTimeout)
	votekeys, err := GetAssociatedVoteAccounts(ctx, client, rpc.CommitmentFinalized, config.NodeKeys)
	if err != nil {
		logger.Fatalf("Failed to get associated vote accounts for %v: %v", config.NodeKeys, err)
	}
	identity, err := client.GetIdentity(ctx)
	if err != nil {
		logger.Fatalf("Failed to get identity: %v", err)
	}
	collector := NewSolanaCollector(
		client, slotPacerSchedule, config.BalanceAddresses, config.NodeKeys, votekeys, identity,
	)
	slotWatcher := NewSlotWatcher(
		client, config.NodeKeys, votekeys, identity, config.ComprehensiveSlotTracking, config.MonitorBlockSizes,
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go slotWatcher.WatchSlots(ctx, collector.slotPace)

	prometheus.MustRegister(collector)
	http.Handle("/metrics", promhttp.Handler())

	logger.Infof("listening on %s", config.ListenAddress)
	logger.Fatal(http.ListenAndServe(config.ListenAddress, nil))
}
