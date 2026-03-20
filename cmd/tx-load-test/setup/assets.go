package setup

import (
	"context"

	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/config"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
)

// benchmarkAssetCodes are the fixed codes for the 3 classic benchmark assets.
var benchmarkAssetCodes = [3]string{"BLTA", "BLTB", "BLTC"}

type assetsStep struct{}

func (assetsStep) Name() string { return "create benchmark assets" }

// Run registers the 3 benchmark assets in st, using the fee-payer account
// as the issuer for all of them. No on-chain work is needed at this stage:
// the fee-payer already exists, and trustlines + payments are handled by the
// accounts step.
func (assetsStep) Run(_ context.Context, logger *log.Entry, _ config.Config, st *state.State) error {
	for i, code := range benchmarkAssetCodes {
		st.Assets[i] = txnbuild.CreditAsset{Code: code, Issuer: st.FeePayerKP.Address()}
		logger.Infof("asset[%d]: code=%s issuer=%s (fee payer)", i, code, st.FeePayerKP.Address())
	}
	return nil
}
