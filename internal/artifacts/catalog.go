// Package artifacts manages the optional Assistant/Voice supply chain.
// Model payloads never belong to the source repository or application database.
package artifacts

import "runtime"

const ReleaseTag = "models-v1"
const ManifestURL = "https://github.com/sPROFFEs/PrAImate/releases/download/" + ReleaseTag + "/manifest.json"

type Definition struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Kind             string `json:"kind"`
	ArtifactID       string `json:"artifact_id"`
	RuntimeID        string `json:"runtime_id,omitempty"`
	ApproximateBytes int64  `json:"approximate_bytes"`
	Context          int    `json:"default_context,omitempty"`
	Output           int    `json:"default_output,omitempty"`
	PromptProfile    string `json:"prompt_profile,omitempty"`
}

func Catalog() []Definition {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	llama := "runtime/llama.cpp/" + platform + "/v1"
	whisper := "runtime/whisper.cpp/" + platform + "/v1"
	return []Definition{
		{ID: "lfm-efficient", Name: "LFM2.5 350M · Efficient", Description: "QAD Q4_0 · Short application commands and structured actions", Kind: "assistant", ArtifactID: "assistant/lfm2.5-350m/qad-q4_0/v1", RuntimeID: llama, ApproximateBytes: 219000000, Context: 2048, Output: 512, PromptProfile: "assistant-lfm-v1"},
		{ID: "qwen-quality", Name: "Qwen3.5 0.8B · Quality", Description: "Q4_0 · Multilingual requests and more complex operations", Kind: "assistant", ArtifactID: "assistant/qwen3.5-0.8b/q4_0/v1", RuntimeID: llama, ApproximateBytes: 563000000, Context: 4096, Output: 768, PromptProfile: "assistant-qwen-v1"},
		{ID: "whisper-tiny", Name: "Whisper Tiny Q5_1", Description: "Lowest resource usage", Kind: "speech", ArtifactID: "speech/whisper-tiny/q5_1/v1", RuntimeID: whisper, ApproximateBytes: 31 << 20},
		{ID: "whisper-base", Name: "Whisper Base Q5_1", Description: "Balanced speech recognition", Kind: "speech", ArtifactID: "speech/whisper-base/q5_1/v1", RuntimeID: whisper, ApproximateBytes: 57 << 20},
		{ID: "whisper-small", Name: "Whisper Small Q5_1", Description: "Higher transcription accuracy", Kind: "speech", ArtifactID: "speech/whisper-small/q5_1/v1", RuntimeID: whisper, ApproximateBytes: 181 << 20},
		{ID: "llama-runtime", Name: "llama.cpp runtime", Description: platform + " · Pinned inference runtime", Kind: "runtime", ArtifactID: llama},
		{ID: "whisper-runtime", Name: "whisper.cpp runtime", Description: platform + " · Pinned speech runtime", Kind: "runtime", ArtifactID: whisper},
	}
}
