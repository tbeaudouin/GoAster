Telegram voice transcription with Groq

Contributor fork: tbeaudouin/GoAster
Branch: goaster-customization
Revision: c3278c884b6d380fdabf984ab132d41625ec845d
Implementation: internal/telegram/voice.go and internal/telegram/adapter.go
Tests: internal/telegram/voice_test.go and internal/telegram/voice_group_order_test.go

Transcribes Telegram voice notes using Groq’s whisper-large-v3-turbo model, displays the transcript in the conversation, and sends it to the agent as text. Configure GROQ_API_KEY through GoAster’s protected environment flow. Voice notes are limited to 20 MiB and 10 minutes; audio is sent to Groq for transcription.
