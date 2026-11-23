# Cron tool schema nullability

## Reproduction

Before this change the model-visible `cron` schema declared `at_seconds` and `every_seconds` as integers with minimum 1, with no null alternative. A recurring call such as `every_seconds: 1800` therefore could not supply `at_seconds: null` under that schema. If all fields were required by strict schema handling, the model had to choose a positive value for both, which the handler correctly rejects as conflicting schedules. The previous handler-only change did not change the emitted schema. The Responses API documentation describes strict schemas as requiring every property and using null for optional properties, and says schemas may be normalized to strict when `strict` is omitted; no live endpoint behavior or historical provider normalization was independently verified here. Source: https://developers.openai.com/api/docs/guides/function-calling

## Design

Both normal and scheduled cron definitions require all eight keys, retain a non-null action enum, and permit null for every other field. Schedule integers retain minimum 1, while `max_runs` retains minimum 0. Null means absent, including on updates; the existing handler still accepts legacy zero schedule placeholders on add, rejects them on update, and preserves explicit false and zero values. Scheduled definitions continue to exclude add. No global tool schema or strict setting changed.

## Validation and limits

Offline tests capture actual provider HTTP JSON using a local fake endpoint, inspect both normal and genuinely scheduled registry variants, validate the recurring strict-shaped input against the emitted schema vocabulary, dispatch it through the real handler, and check the final confirmation and exactly one synthetic job. A temporary scheduler and synthetic executor exercise the scheduled context; no live provider, cron service, migration, or server was used. Additional tests cover other actions and schedules, null patches, explicit false/zero, invalid inputs without mutation, and existing diagnostic privacy. The checks do not establish live provider normalization or acceptance.

Commands: `/usr/local/go/bin/go test ./internal/chat ./internal/scheduler`; `/usr/local/go/bin/go test -race ./internal/chat ./internal/scheduler -run 'TestCron|TestScheduledCron' -count=1`; `/usr/local/go/bin/go vet ./...`; `/usr/local/go/bin/go build ./...`; `git diff --check`.

Final acceptance: independent Terra full-diff review returned SATISFIED. Parent reran `/usr/local/go/bin/go test ./internal/chat ./internal/scheduler ./internal/diagnostics ./internal/telegram`, `/usr/local/go/bin/go vet ./...`, `/usr/local/go/bin/go build ./...`, and `git diff --check` on final code; all passed. No full-suite run or live provider acceptance test; running agent remains unchanged until separately upgraded.
