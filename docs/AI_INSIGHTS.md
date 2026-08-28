# AI Insights

Kerneon works without AI. Local Insights are always available and all telemetry stays on the PC unless you explicitly connect an OpenAI API key and click **Generate insights**.

## Connect

1. Create or select an API key in the [OpenAI platform](https://platform.openai.com/api-keys).
2. Copy the key.
3. Open **Insights** in Kerneon and choose **Connect copied API key**.
4. Choose **Generate insights** whenever you want a fresh analysis.

The key is saved as `Kerneon/OpenAI API Key` in Windows Credential Manager. It is not stored in `settings.json`, logs, diagnostics, or the application folder. **Disconnect** removes Kerneon's stored credential. API usage is charged separately by OpenAI; it is not included with this portable build.

## Data boundary

Each requested analysis contains current values, up to 60 seconds of aggregate resource history, and metrics for up to five anonymous process rows (`process_1`, etc.). It does not contain process names, executable paths, hostnames, usernames, adapter names, or IP addresses.

Requests use GPT-5.4 Mini through the Responses API, set `store` to `false`, request strict structured JSON, and provide no tools. The model can write advisory cards only; it cannot invoke Optimize, run commands, access files, or control the PC.

OpenAI's current data-controls documentation says API data is not used for training by default and describes abuse-monitoring retention and eligible organization controls. Review the [official data controls](https://developers.openai.com/api/docs/guides/your-data) before enabling AI if the telemetry is sensitive.

