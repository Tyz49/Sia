package consensus

// A DiffDirection indicates the "direction" of a diff, either applied or
// reverted. A bool is used to restrict the value to these two possibilities.
type DiffDirection bool

const (
	DiffApply  DiffDirection = true
	DiffRevert DiffDirection = false
)

// A SiacoinOutputDiff indicates the addition or removal of a SiacoinOutput in
// the consensus set.
type SiacoinOutputDiff struct {
	Direction     DiffDirection
	ID            SiacoinOutputID
	SiacoinOutput SiacoinOutput
}

// A FileContractDiff indicates the addition or removal of a FileContract in
// the consensus set.
type FileContractDiff struct {
	Direction    DiffDirection
	ID           FileContractID
	FileContract FileContract
}

// A SiafundOutputDiff indicates the addition or removal of a SiafundOutput in
// the consensus set.
type SiafundOutputDiff struct {
	Direction     DiffDirection
	ID            SiafundOutputID
	SiafundOutput SiafundOutput
}

// A SiafundPoolDiff contains the value of the siafundPool before the block
// was applied, and after the block was applied. When applying the diff, set
// siafundPool to 'Adjusted'. When reverting the diff, set siafundPool to
// 'Previous'.
type SiafundPoolDiff struct {
	Previous Currency
	Adjusted Currency
}

// commitSiacoinOutputDiff applies or reverts a SiacoinOutputDiff.
func (s *State) commitSiacoinOutputDiff(scod SiacoinOutputDiff, dir DiffDirection) {
	// Sanity check - should not be adding an output twice, or deleting an
	// output that does not exist.
	if DEBUG {
		_, exists := s.siacoinOutputs[scod.ID]
		if exists == (scod.Direction == dir) {
			panic("rogue siacoin output in commitSiacoinOutputDiff")
		}
	}

	if scod.Direction == dir {
		s.siacoinOutputs[scod.ID] = scod.SiacoinOutput
	} else {
		delete(s.siacoinOutputs, scod.ID)
	}
}

// commitFileContractDiff applies or reverts a FileContractDiff.
func (s *State) commitFileContractDiff(fcd FileContractDiff, dir DiffDirection) {
	// Sanity check - should not be adding a contract twice, or deleting a
	// contract that does not exist.
	if DEBUG {
		_, exists := s.fileContracts[fcd.ID]
		if exists == (fcd.Direction == dir) {
			panic("rogue file contract in commitFileContractDiff")
		}
	}

	if fcd.Direction == dir {
		s.fileContracts[fcd.ID] = fcd.FileContract
	} else {
		delete(s.fileContracts, fcd.ID)
	}
}

// commitSiafundOutputDiff applies or reverts a SiafundOutputDiff.
func (s *State) commitSiafundOutputDiff(sfod SiafundOutputDiff, dir DiffDirection) {
	// Sanity check - should not be adding an output twice, or deleting an
	// output that does not exist.
	if DEBUG {
		_, exists := s.siafundOutputs[sfod.ID]
		if exists == (sfod.Direction == dir) {
			panic("rogue siafund output in commitSiafundOutputDiff")
		}
	}

	if sfod.Direction == dir {
		s.siafundOutputs[sfod.ID] = sfod.SiafundOutput
	} else {
		delete(s.siafundOutputs, sfod.ID)
	}
}

// commitSiafundPoolDiff applies or reverts a SiafundPoolDiff.
func (s *State) commitSiafundPoolDiff(sfpd SiafundPoolDiff, dir DiffDirection) {
	if dir == DiffApply {
		s.siafundPool = sfpd.Adjusted
	} else {
		s.siafundPool = sfpd.Previous
	}
}

// commitDiffSet applies or reverts the diffs in a blockNode.
func (s *State) commitDiffSet(bn *blockNode, dir DiffDirection) {
	// Sanity check
	if DEBUG {
		// Diffs should have already been generated for this node.
		if !bn.diffsGenerated {
			panic("misuse of applyDiffSet - diffs have not been generated!")
		}

		// Current node must be the input node's parent if applying, and
		// current node must be the input node if reverting.
		if dir == DiffApply {
			if bn.parent.block.ID() != s.currentBlockID {
				panic("applying a block node when it's not a valid successor")
			}
		} else {
			if bn.block.ID() != s.currentBlockID {
				panic("applying a block node when it's not a valid successor")
			}
		}
	}

	// apply each of the diffs
	for _, scod := range bn.siacoinOutputDiffs {
		s.commitSiacoinOutputDiff(scod, dir)
	}
	for _, fcd := range bn.fileContractDiffs {
		s.commitFileContractDiff(fcd, dir)
	}
	for _, sfod := range bn.siafundOutputDiffs {
		s.commitSiafundOutputDiff(sfod, dir)
	}
	s.commitSiafundPoolDiff(bn.siafundPoolDiff, dir)

	// update State metadata
	if dir == DiffApply {
		s.currentBlockID = bn.block.ID()

		s.currentPath[bn.height] = bn.block.ID()
		s.delayedSiacoinOutputs[bn.height] = bn.delayedSiacoinOutputs
	} else {
		s.currentBlockID = bn.parent.block.ID()

		delete(s.currentPath, bn.height)
		delete(s.delayedSiacoinOutputs, bn.height)
	}
}

// generateAndApplyDiff will verify the block and then integrate it into the
// consensus state. These two actions must happen at the same time because
// transactions are allowed to depend on each other. We can't be sure that a
// transaction is valid unless we have applied all of the previous transactions
// in the block, which means we need to apply while we verify.
func (s *State) generateAndApplyDiff(bn *blockNode) (err error) {
	// Sanity check
	if DEBUG {
		// Generate should only be called if the diffs have not yet been
		// generated.
		if bn.diffsGenerated {
			panic("misuse of generateAndApplyDiff")
		}

		// Current node must be the input node's parent.
		if bn.parent.block.ID() != s.currentBlockID {
			panic("applying a block node when it's not a valid successor")
		}
	}

	// Update the state to point to the new block.
	s.currentBlockID = bn.block.ID()
	s.currentPath[bn.height] = bn.block.ID()
	s.delayedSiacoinOutputs[s.height()] = make(map[SiacoinOutputID]SiacoinOutput)

	// diffsGenerated is set to true as soon as we start changing the set of
	// diffs in the block node. If at any point the block is found to be
	// invalid, the diffs can be safely reversed from whatever point.
	bn.diffsGenerated = true

	// The first diff to be applied is to mark what the starting siafundPool balance
	// is.
	bn.siafundPoolDiff.Previous = s.siafundPool

	// Validate and apply each transaction in the block. They cannot be
	// validated all at once because some transactions may not be valid until
	// previous transactions have been applied.
	for _, txn := range bn.block.Transactions {
		err = s.validTransaction(txn)
		if err != nil {
			s.invalidateNode(bn)
			s.commitDiffSet(bn, DiffRevert)
			return
		}

		s.applyTransaction(bn, txn)
	}

	// After all of the transactions have been applied, 'maintenance' is
	// applied on the block. This includes adding any outputs that have reached
	// maturity, applying any contracts with missed storage proofs, and adding
	// the miner payouts to the list of delayed outputs.
	s.applyMaintenance(bn)

	// The final thing is to update the siafundPoolDiff to indicate where the
	// siafund pool ended up.
	bn.siafundPoolDiff.Adjusted = s.siafundPool

	return
}
