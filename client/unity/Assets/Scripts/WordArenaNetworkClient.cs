using System;
using System.Collections;
using System.IO;
using System.Net.WebSockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using UnityEngine;
using UnityEngine.Networking;

namespace Words.Client
{
    public sealed class WordArenaNetworkClient : IDisposable
    {
        private const int ReceiveChunkBytes = 8192;
        private ClientWebSocket socket;
        private CancellationTokenSource cancellation;
        private Task receiveTask;
        private Task sendTask;

        public event Action<string> StatusChanged;
        public event Action<WordArenaSnapshot> SnapshotReceived;
        public event Action<WordArenaValidatedEvent> WordEventReceived;

        public bool IsConnected
        {
            get
            {
                var active = socket;
                return active != null && active.State == WebSocketState.Open;
            }
        }

        public IEnumerator CreateMatch(string serverBaseUrl, string language, bool suddenDeath, Action<CreateMatchResult> completed)
        {
            var url = NormalizeHttpBase(serverBaseUrl) + "/v1/matches";
            var body = "{\"language\":\"" + EscapeJson(language) + "\",\"sudden_death\":" + (suddenDeath ? "true" : "false") + "}";
            var request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST);
            var payload = Encoding.UTF8.GetBytes(body);
            request.uploadHandler = new UploadHandlerRaw(payload);
            request.downloadHandler = new DownloadHandlerBuffer();
            request.SetRequestHeader("Content-Type", "application/json");
            request.timeout = 10;

            EmitStatus("Creating server match at " + url);
            yield return request.SendWebRequest();

            var result = new CreateMatchResult();
            var ok = false;
#if UNITY_2020_2_OR_NEWER
            ok = request.result == UnityWebRequest.Result.Success;
#else
            ok = !request.isNetworkError && !request.isHttpError;
#endif
            if (!ok)
            {
                result.Error = "create match failed: HTTP " + request.responseCode + " " + request.error + " " + request.downloadHandler.text;
                EmitStatus(result.Error);
                request.Dispose();
                if (completed != null)
                {
                    completed(result);
                }
                yield break;
            }

            try
            {
                var parsed = JsonUtility.FromJson<CreateMatchResponseJson>(request.downloadHandler.text);
                if (parsed == null || parsed.tokens == null || parsed.tokens.Length < 2 || parsed.user_ids == null || parsed.user_ids.Length < 2)
                {
                    result.Error = "create match returned incomplete JSON";
                }
                else
                {
                    result.Success = true;
                    result.MatchId = (ulong)Math.Max(0L, parsed.match_id);
                    result.Seed = (ulong)Math.Max(0L, parsed.seed);
                    result.Language = parsed.language;
                    result.SuddenDeath = parsed.sudden_death;
                    result.Tokens = parsed.tokens;
                    result.UserIds = new ulong[] { (ulong)Math.Max(0L, parsed.user_ids[0]), (ulong)Math.Max(0L, parsed.user_ids[1]) };
                }
            }
            catch (Exception exception)
            {
                result.Error = "create match JSON parse failed: " + exception.Message;
            }
            finally
            {
                request.Dispose();
            }

            if (result.Success)
            {
                EmitStatus("Created match " + result.MatchId + "; connecting seat 0.");
            }
            else
            {
                EmitStatus(result.Error);
            }

            if (completed != null)
            {
                completed(result);
            }
        }


        public IEnumerator CheckReadyz(string serverBaseUrl, Action<ReadyzResult> completed)
        {
            var url = NormalizeHttpBase(serverBaseUrl) + "/readyz";
            var request = UnityWebRequest.Get(url);
            request.timeout = 5;
            EmitStatus("Checking backend readiness.");
            yield return request.SendWebRequest();

            var result = new ReadyzResult();
            result.HttpStatus = request.responseCode;
            result.Success = RequestSucceeded(request);
            try
            {
                var parsed = JsonUtility.FromJson<ReadyzResponseJson>(request.downloadHandler.text);
                if (parsed != null)
                {
                    result.Status = parsed.status;
                    result.Storage = parsed.storage;
                    result.ActiveMatches = parsed.active_matches;
                    result.Error = parsed.error;
                }
            }
            catch (Exception exception)
            {
                result.Error = "ready JSON parse failed: " + exception.Message;
            }

            if (string.IsNullOrEmpty(result.Status))
            {
                result.Status = result.Success ? "ready" : "not_ready";
            }

            if (!result.Success && string.IsNullOrEmpty(result.Error))
            {
                result.Error = request.error;
            }

            request.Dispose();
            EmitStatus("Backend readiness: " + result.DisplayText);
            if (completed != null)
            {
                completed(result);
            }
        }

        public IEnumerator RotateSeatToken(string serverBaseUrl, ulong matchId, string currentToken, Action<RotateTokenResult> completed)
        {
            var url = NormalizeHttpBase(serverBaseUrl) + "/v1/matches/" + matchId + "/token/rotate";
            var body = "{\"token\":\"" + EscapeJson(currentToken) + "\"}";
            var request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST);
            var payload = Encoding.UTF8.GetBytes(body);
            request.uploadHandler = new UploadHandlerRaw(payload);
            request.downloadHandler = new DownloadHandlerBuffer();
            request.SetRequestHeader("Content-Type", "application/json");
            request.timeout = 10;

            EmitStatus("Rotating active seat token.");
            yield return request.SendWebRequest();

            var result = new RotateTokenResult();
            result.HttpStatus = request.responseCode;
            result.Success = RequestSucceeded(request);
            if (!result.Success)
            {
                result.Error = "token rotation failed: HTTP " + request.responseCode + " " + request.error;
            }
            else
            {
                try
                {
                    var parsed = JsonUtility.FromJson<RotateTokenResponseJson>(request.downloadHandler.text);
                    if (parsed == null || string.IsNullOrEmpty(parsed.token))
                    {
                        result.Success = false;
                        result.Error = "token rotation returned incomplete JSON";
                    }
                    else
                    {
                        result.MatchId = (ulong)Math.Max(0L, parsed.match_id);
                        result.Seat = parsed.seat;
                        result.UserId = (ulong)Math.Max(0L, parsed.user_id);
                        result.Token = parsed.token;
                    }
                }
                catch (Exception exception)
                {
                    result.Success = false;
                    result.Error = "token rotation JSON parse failed: " + exception.Message;
                }
            }

            request.Dispose();
            EmitStatus(result.Success ? "Active seat token rotated." : result.Error);
            if (completed != null)
            {
                completed(result);
            }
        }

        public IEnumerator FetchResult(string serverBaseUrl, ulong matchId, Action<MatchResultSummary> completed)
        {
            var url = NormalizeHttpBase(serverBaseUrl) + "/v1/matches/" + matchId + "/result";
            var request = UnityWebRequest.Get(url);
            request.timeout = 10;
            EmitStatus("Fetching authoritative match result.");
            yield return request.SendWebRequest();

            var result = new MatchResultSummary();
            result.HttpStatus = request.responseCode;
            result.Success = RequestSucceeded(request);
            if (!result.Success)
            {
                result.Error = "result unavailable: HTTP " + request.responseCode + " " + request.error;
            }
            else
            {
                try
                {
                    var parsed = JsonUtility.FromJson<MatchResultResponseJson>(request.downloadHandler.text);
                    if (parsed == null || parsed.scores == null || parsed.scores.Length < 2)
                    {
                        result.Success = false;
                        result.Error = "result endpoint returned incomplete JSON";
                    }
                    else
                    {
                        result.MatchId = (ulong)Math.Max(0L, parsed.match_id);
                        result.Seed = (ulong)Math.Max(0L, parsed.seed);
                        result.Language = parsed.language;
                        result.Over = parsed.over;
                        result.WinnerSeat = parsed.winner_seat;
                        result.IsTie = parsed.is_tie;
                        result.Scores = parsed.scores;
                        result.StateVersion = (uint)Math.Max(0, parsed.state_version);
                        result.ServerTick = (uint)Math.Max(0, parsed.server_tick);
                    }
                }
                catch (Exception exception)
                {
                    result.Success = false;
                    result.Error = "result JSON parse failed: " + exception.Message;
                }
            }

            request.Dispose();
            EmitStatus(result.Success ? "Fetched authoritative match result." : result.Error);
            if (completed != null)
            {
                completed(result);
            }
        }

        public void Connect(string serverBaseUrl, ulong matchId, string token)
        {
            Disconnect();
            cancellation = new CancellationTokenSource();
            socket = new ClientWebSocket();
            var uri = BuildWebSocketUri(serverBaseUrl, matchId, token);
            receiveTask = ConnectAndReceive(uri, cancellation.Token);
        }

        public void SubmitWord(ulong matchId, uint clientSequence, int[] selectedCellIds)
        {
            if (selectedCellIds == null || selectedCellIds.Length == 0)
            {
                EmitStatus("No cells selected.");
                return;
            }

            var active = socket;
            if (active == null || active.State != WebSocketState.Open)
            {
                EmitStatus("Not connected to match WebSocket.");
                return;
            }

            var payload = WordArenaProto.EncodeSubmitWord(matchId, clientSequence, selectedCellIds, UnixTimeMilliseconds());
            sendTask = Send(active, payload, cancellation == null ? CancellationToken.None : cancellation.Token);
        }

        public void Disconnect()
        {
            var existingCancellation = cancellation;
            cancellation = null;
            if (existingCancellation != null)
            {
                existingCancellation.Cancel();
                existingCancellation.Dispose();
            }

            if (receiveTask != null && receiveTask.IsFaulted)
            {
                EmitStatus("Previous receive task faulted: " + receiveTask.Exception.GetBaseException().Message);
            }

            if (sendTask != null && sendTask.IsFaulted)
            {
                EmitStatus("Previous send task faulted: " + sendTask.Exception.GetBaseException().Message);
            }

            receiveTask = null;
            sendTask = null;

            var existingSocket = socket;
            socket = null;
            if (existingSocket != null)
            {
                try
                {
                    existingSocket.Abort();
                    existingSocket.Dispose();
                }
                catch (Exception)
                {
                    // Best-effort cleanup only; reconnect path creates a fresh socket.
                }
            }
        }

        public void Dispose()
        {
            Disconnect();
        }

        private async Task ConnectAndReceive(Uri uri, CancellationToken token)
        {
            try
            {
                EmitStatus("Connecting " + RedactedUri(uri));
                var active = socket;
                if (active == null)
                {
                    return;
                }

                await active.ConnectAsync(uri, token);
                EmitStatus("Connected to authoritative match stream.");
                await ReceiveLoop(active, token);
            }
            catch (OperationCanceledException)
            {
                EmitStatus("Disconnected from match stream.");
            }
            catch (Exception exception)
            {
                EmitStatus("WebSocket error: " + exception.Message);
            }
        }

        private async Task ReceiveLoop(ClientWebSocket active, CancellationToken token)
        {
            var chunk = new byte[ReceiveChunkBytes];
            while (!token.IsCancellationRequested && active.State == WebSocketState.Open)
            {
                using (var message = new MemoryStream())
                {
                    WebSocketReceiveResult result;
                    do
                    {
                        result = await active.ReceiveAsync(new ArraySegment<byte>(chunk), token);
                        if (result.MessageType == WebSocketMessageType.Close)
                        {
                            EmitStatus("Server closed match stream.");
                            return;
                        }

                        message.Write(chunk, 0, result.Count);
                    }
                    while (!result.EndOfMessage);

                    if (result.MessageType != WebSocketMessageType.Binary)
                    {
                        continue;
                    }

                    var bytes = message.ToArray();
                    WordArenaServerEnvelope envelope;
                    string error;
                    if (!WordArenaProto.TryDecodeServerEnvelope(bytes, bytes.Length, out envelope, out error))
                    {
                        EmitStatus("Ignoring undecodable server frame: " + error);
                        continue;
                    }

                    if (envelope.HasSnapshot)
                    {
                        var snapshot = envelope.Snapshot;
                        if (snapshot.MatchId == 0)
                        {
                            snapshot.MatchId = envelope.MatchId;
                        }

                        EmitSnapshot(snapshot);
                    }
                    else if (envelope.HasWordEvent)
                    {
                        EmitWordEvent(envelope.WordEvent);
                    }
                }
            }
        }

        private async Task Send(ClientWebSocket active, byte[] payload, CancellationToken token)
        {
            try
            {
                await active.SendAsync(new ArraySegment<byte>(payload), WebSocketMessageType.Binary, true, token);
                EmitStatus("Submitted intent to authoritative server.");
            }
            catch (Exception exception)
            {
                EmitStatus("Submit failed: " + exception.Message);
            }
        }

        private void EmitStatus(string message)
        {
            var handler = StatusChanged;
            if (handler != null)
            {
                handler(message);
            }
        }

        private void EmitSnapshot(WordArenaSnapshot snapshot)
        {
            var handler = SnapshotReceived;
            if (handler != null)
            {
                handler(snapshot);
            }
        }

        private void EmitWordEvent(WordArenaValidatedEvent wordEvent)
        {
            var handler = WordEventReceived;
            if (handler != null)
            {
                handler(wordEvent);
            }
        }


        private static bool RequestSucceeded(UnityWebRequest request)
        {
#if UNITY_2020_2_OR_NEWER
            return request.result == UnityWebRequest.Result.Success;
#else
            return !request.isNetworkError && !request.isHttpError;
#endif
        }

        private static string RedactedUri(Uri uri)
        {
            if (uri == null)
            {
                return string.Empty;
            }

            var builder = new UriBuilder(uri);
            builder.Query = "match_id=<id>&token=<redacted>";
            return builder.Uri.ToString();
        }

        private static Uri BuildWebSocketUri(string serverBaseUrl, ulong matchId, string token)
        {
            var baseUri = new Uri(NormalizeHttpBase(serverBaseUrl));
            var builder = new UriBuilder(baseUri);
            if (builder.Scheme == "https")
            {
                builder.Scheme = "wss";
            }
            else if (builder.Scheme == "http")
            {
                builder.Scheme = "ws";
            }

            var prefix = builder.Path == null || builder.Path == "/" ? string.Empty : builder.Path.TrimEnd('/');
            builder.Path = prefix + "/v1/match/ws";
            builder.Query = "match_id=" + matchId + "&token=" + Uri.EscapeDataString(token == null ? string.Empty : token);
            return builder.Uri;
        }

        private static string NormalizeHttpBase(string serverBaseUrl)
        {
            var value = string.IsNullOrWhiteSpace(serverBaseUrl) ? "http://127.0.0.1:18080" : serverBaseUrl.Trim();
            if (value.StartsWith("ws://", StringComparison.OrdinalIgnoreCase))
            {
                value = "http://" + value.Substring("ws://".Length);
            }
            else if (value.StartsWith("wss://", StringComparison.OrdinalIgnoreCase))
            {
                value = "https://" + value.Substring("wss://".Length);
            }
            else if (value.IndexOf("://", StringComparison.Ordinal) < 0)
            {
                value = "http://" + value;
            }

            return value.TrimEnd('/');
        }

        private static string EscapeJson(string value)
        {
            if (value == null)
            {
                return string.Empty;
            }

            return value.Replace("\\", "\\\\").Replace("\"", "\\\"");
        }

        private static ulong UnixTimeMilliseconds()
        {
            var epoch = new DateTime(1970, 1, 1, 0, 0, 0, DateTimeKind.Utc);
            return (ulong)Math.Max(0.0, (DateTime.UtcNow - epoch).TotalMilliseconds);
        }


        [Serializable]
        private sealed class ReadyzResponseJson
        {
            public string status;
            public string storage;
            public int active_matches;
            public string error;
        }

        [Serializable]
        private sealed class RotateTokenResponseJson
        {
            public long match_id;
            public int seat;
            public long user_id;
            public string token;
        }

        [Serializable]
        private sealed class MatchResultResponseJson
        {
            public long match_id;
            public long seed;
            public string language;
            public bool over;
            public int winner_seat;
            public bool is_tie;
            public int[] scores;
            public int state_version;
            public int server_tick;
        }

        [Serializable]
        private sealed class CreateMatchResponseJson
        {
            public long match_id;
            public long seed;
            public string language;
            public bool sudden_death;
            public string[] tokens;
            public long[] user_ids;
        }
    }

    public sealed class CreateMatchResult
    {
        public bool Success;
        public string Error = string.Empty;
        public ulong MatchId;
        public ulong Seed;
        public string Language = string.Empty;
        public bool SuddenDeath;
        public string[] Tokens = new string[0];
        public ulong[] UserIds = new ulong[0];
    }

    public sealed class ReadyzResult
    {
        public bool Success;
        public long HttpStatus;
        public string Status = string.Empty;
        public string Storage = string.Empty;
        public int ActiveMatches;
        public string Error = string.Empty;

        public string DisplayText
        {
            get
            {
                var storageText = string.IsNullOrEmpty(Storage) ? "unknown" : Storage;
                var text = Status + " storage=" + storageText + " active=" + ActiveMatches;
                if (!string.IsNullOrEmpty(Error))
                {
                    text += " error=" + Error;
                }

                return text;
            }
        }
    }

    public sealed class RotateTokenResult
    {
        public bool Success;
        public long HttpStatus;
        public string Error = string.Empty;
        public ulong MatchId;
        public int Seat;
        public ulong UserId;
        public string Token = string.Empty;
    }

    public sealed class MatchResultSummary
    {
        public bool Success;
        public long HttpStatus;
        public string Error = string.Empty;
        public ulong MatchId;
        public ulong Seed;
        public string Language = string.Empty;
        public bool Over;
        public int WinnerSeat;
        public bool IsTie;
        public int[] Scores = new int[0];
        public uint StateVersion;
        public uint ServerTick;

        public string DisplayText
        {
            get
            {
                if (!Success)
                {
                    return string.IsNullOrEmpty(Error) ? "result unavailable" : Error;
                }

                var scoreText = Scores != null && Scores.Length >= 2 ? Scores[0] + ":" + Scores[1] : "?:?";
                var winnerText = IsTie ? "tie" : "winner seat " + WinnerSeat;
                return "match=" + MatchId + " " + scoreText + " " + winnerText + " version=" + StateVersion;
            }
        }
    }

}
