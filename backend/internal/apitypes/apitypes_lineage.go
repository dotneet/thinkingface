package apitypes

// ---------------------------------------------------------------- lineage

// LineageEdgeKind names the sort of provenance one lineage edge records.
type LineageEdgeKind string

const (
	// LineageEdgeKindDataset points at a dataset the repository was built from.
	LineageEdgeKindDataset LineageEdgeKind = "dataset"
	// LineageEdgeKindBaseModel points at the checkpoint it started from.
	LineageEdgeKindBaseModel LineageEdgeKind = "base_model"
	// LineageEdgeKindRun points at the experiment run that produced it.
	LineageEdgeKindRun LineageEdgeKind = "run"
	// LineageEdgeKindEvalDataset points at a dataset the repository was
	// evaluated on, which is a different claim from having trained on it.
	LineageEdgeKindEvalDataset LineageEdgeKind = "eval_dataset"
	// LineageEdgeKindNewVersion points at the repository that supersedes this
	// one. It is the only kind that targets a repository of its own kind, and
	// it does not appear in RepoLineageResponse.Upstream: the resolved chain
	// in NewVersion says the same thing more usefully.
	LineageEdgeKindNewVersion LineageEdgeKind = "new_version"
)

// LineageRelation names how a repository relates to the base model it points
// at -- HuggingFace Hub's `base_model_relation`. A card may declare it
// outright; when it does not, the sync worker infers it from the repository's
// contents (docs/dev/api-contract.md §12).
//
// The wire fields carrying it are plain strings, not this type: a card is free
// to write something outside the four known values, and such a value is passed
// through verbatim rather than being rewritten into a lie. These constants are
// the set the UI groups by; everything else belongs under "other".
type LineageRelation string

const (
	// LineageRelationFinetune is further training from the base model's own
	// weights. It is the default when nothing more specific applies.
	LineageRelationFinetune LineageRelation = "finetune"
	// LineageRelationAdapter is a LoRA/PEFT adapter over the base model.
	LineageRelationAdapter LineageRelation = "adapter"
	// LineageRelationQuantized is the base model at a lower precision.
	LineageRelationQuantized LineageRelation = "quantized"
	// LineageRelationMerge is a blend of two or more base models.
	LineageRelationMerge LineageRelation = "merge"
)

// LineageRef is one upstream reference a repository card declares.
type LineageRef struct {
	Kind LineageEdgeKind `json:"kind"`
	// Raw is the reference exactly as the card spelled it, e.g.
	// "team/imdb-ja@v1". It is the only field worth showing when Exists is
	// false.
	Raw string `json:"raw"`
	// TargetKind is the repository kind this edge points at. Dataset and run
	// edges both target dataset repositories: experiment logs live in one.
	TargetKind RepoKind `json:"target_kind"`
	// Namespace, Name and FullName are the normalised target, all "" when the
	// raw reference does not parse as one.
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	// Rev is the branch, tag or commit the reference pinned, "" for none.
	Rev string `json:"rev"`
	// Project and Run are set on run edges only.
	Project string `json:"project"`
	Run     string `json:"run"`
	// Relation is how this repository relates to the base model it names --
	// one of LineageRelation, or whatever else the card declared. Set on
	// base_model edges only; "" on dataset and run edges.
	Relation string `json:"relation"`
	// Exists reports that the target resolves to a repository. A false value
	// means the UI must render plain text, not a link: the reference may be a
	// typo, or may not have been pushed yet.
	Exists bool `json:"exists"`
}

// LineageDependent is one repository that names this repository -- or one of
// its runs -- as part of its own origin.
type LineageDependent struct {
	Repo RepoSummary     `json:"repo"`
	Kind LineageEdgeKind `json:"kind"`
	// Raw is the dependent's own reference string, which shows how it pinned
	// this repository.
	Raw string `json:"raw"`
	Rev string `json:"rev"`
	// Project and Run name the run the dependent came from, on run edges only.
	Project string `json:"project"`
	Run     string `json:"run"`
	// Relation is how the dependent describes itself relative to this
	// repository -- one of LineageRelation, or whatever else its card
	// declared. Set on base_model edges only; "" on dataset and run edges.
	// It is what the model tree groups the derived repositories by.
	Relation string `json:"relation"`
}

// LineageSuccessor is where a repository's `new_version:` declaration leads:
// the successor its own card names, and the end of the chain that successor
// starts (docs/dev/api-contract.md §12).
type LineageSuccessor struct {
	// Direct is the successor this repository's card names outright. It is
	// the only field with anything in it when the reference is dangling, in
	// which case Direct.Exists is false and Hops is 0.
	Direct LineageRef `json:"direct"`
	// Latest is the newest version reachable by following `new_version:` from
	// one repository to the next -- what a reader should be sent to. It
	// equals Direct for a one-hop chain, and also whenever Truncated is set.
	Latest LineageRef `json:"latest"`
	// Hops is how many edges were followed to reach Latest: 1 for a direct
	// successor, 0 when the declared successor does not resolve.
	Hops int `json:"hops" tstype:"number"`
	// Truncated reports that the chain never ended -- it formed a cycle, or
	// ran past the walk's depth limit. Latest is then the direct successor
	// only, and the UI must not claim it is the newest version.
	Truncated bool `json:"truncated" tstype:"boolean"`
}

// RepoLineageResponse is a repository's provenance in both directions.
type RepoLineageResponse struct {
	// Upstream is what this repository's card declares it came from.
	Upstream []LineageRef `json:"upstream"`
	// Downstream is the reverse lookup: repositories whose cards point here.
	Downstream []LineageDependent `json:"downstream"`
	// NewVersion is the successor the card declares, resolved through the
	// chain of successors behind it, or null when the card declares none.
	// Successor edges are reported here instead of in Upstream: they point
	// forward in time, not back at an origin.
	NewVersion *LineageSuccessor `json:"new_version" tstype:"LineageSuccessor | null,required"`
	// ProducedBy lists the experiment runs that declared this repository as
	// their output (`trackio.log_model`). It is separate from the `run` edges
	// in Upstream because the claim comes from the other end: those are what
	// this repository's own card says, these are what a training script said.
	// Empty for anything but a model repository.
	ProducedBy []ExpRunProducer `json:"produced_by"`
}

// ExpRunProducer is one experiment run that declared it produced a model.
type ExpRunProducer struct {
	// Repo is the experiment *dataset* repository the run lives in (§7).
	Repo    RepoSummary `json:"repo"`
	Project string      `json:"project"`
	Run     string      `json:"run"`
	// Revision is the revision of the produced model the run recorded, "" if
	// it could not resolve one.
	Revision string `json:"revision"`
}

// ExpRunLineage lists the repositories one experiment run produced.
type ExpRunLineage struct {
	Run    string             `json:"run"`
	Models []LineageDependent `json:"models"`
}

// ExpLineageResponse carries the run-to-model links of an experiment project.
type ExpLineageResponse struct {
	Items []ExpRunLineage `json:"items"`
}
