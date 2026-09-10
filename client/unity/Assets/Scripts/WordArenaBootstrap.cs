using System;
using System.Collections.Generic;
using UnityEngine;

namespace Words.Client
{
    /// <summary>
    /// M1 client UX bootstrap. It now has two modes:
    ///
    /// 1. Local demo mode keeps the no-server touch affordances from Batch 11.
    /// 2. Server mode creates/connects to a match, submits only player intents,
    ///    and renders canonical snapshots/events from the authoritative Go
    ///    WebSocket protobuf protocol.
    ///
    /// The object is created at runtime and rendered with IMGUI so the minimal
    /// scene can keep building on CI without requiring a Linux/ARM64 Unity
    /// Editor session to serialize GameObjects or uGUI/EventSystem objects.
    /// </summary>
    public sealed class WordArenaBootstrap : MonoBehaviour
    {
        private const float LockSeconds = 3f;
        private const float ComboWindowSeconds = 10f;
        private static readonly string[] DemoLetters = { "T", "A", "A", "N", "D", "S", "V", "T", "G", "C", "O", "D" };
        private static readonly HashSet<string> DemoWords = new HashSet<string>(StringComparer.OrdinalIgnoreCase) { "CAT", "DOG" };

        private readonly List<CellViewModel> cells = new List<CellViewModel>();
        private readonly List<int> selected = new List<int>();
        private readonly PlayerViewModel[] players = { new PlayerViewModel("Blue"), new PlayerViewModel("Orange") };
        private readonly Queue<Action> mainThreadActions = new Queue<Action>();
        private readonly object mainThreadActionsLock = new object();

        private WordArenaNetworkClient network;
        private GUIStyle titleStyle;
        private GUIStyle bannerStyle;
        private GUIStyle hudStyle;
        private GUIStyle statusStyle;
        private GUIStyle cellStyle;
        private GUIStyle actionStyle;
        private GUIStyle inputStyle;
        private int activeSeat;
        private bool suddenDeath;
        private bool serverMode;
        private bool createInProgress;
        private bool readyCheckInProgress;
        private bool tokenRotationInProgress;
        private bool resultFetchInProgress;
        private bool terminalResultFetchRequested;
        private float lastAcceptedAt = -999f;
        private string serverUrl = "http://127.0.0.1:18080";
        private string language = "en";
        private string status = "Demo board ready. Use Create server match to bind this UI to the authoritative backend.";
        private string readySummary = "Ready: not checked";
        private string resultSummary = "Result: not fetched";
        private ulong liveMatchId;
        private ulong liveSeed;
        private uint lastServerTick;
        private uint lastStateVersion;
        private uint clientSequence;
        private string[] liveTokens = new string[2];
        private ulong[] liveUserIds = new ulong[2];

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.AfterSceneLoad)]
        private static void Bootstrap()
        {
            if (FindFirstObjectByType<WordArenaBootstrap>() != null)
            {
                return;
            }

            var host = new GameObject("WordArenaBootstrap");
            DontDestroyOnLoad(host);
            host.AddComponent<WordArenaBootstrap>();
        }

        private void Awake()
        {
            ResetDemoState();
            network = new WordArenaNetworkClient();
            network.StatusChanged += message => EnqueueOnMainThread(() => SetStatus(message));
            network.SnapshotReceived += snapshot => EnqueueOnMainThread(() => ApplyServerSnapshot(snapshot));
            network.WordEventReceived += wordEvent => EnqueueOnMainThread(() => ApplyServerEvent(wordEvent));
        }

        private void OnDestroy()
        {
            if (network != null)
            {
                network.Dispose();
                network = null;
            }
        }

        private void Update()
        {
            DrainMainThreadActions();
            TickVisibleLocks();
        }

        private void OnGUI()
        {
            EnsureStyles();
            var oldMatrix = GUI.matrix;
            var oldBackground = GUI.backgroundColor;
            var scale = Mathf.Min(Screen.width / 1080f, Screen.height / 1920f);
            if (scale <= 0f)
            {
                scale = 1f;
            }

            GUI.matrix = Matrix4x4.TRS(Vector3.zero, Quaternion.identity, new Vector3(scale, scale, 1f));
            var width = Screen.width / scale;
            var height = Screen.height / scale;

            GUILayout.BeginArea(new Rect(40f, 40f, width - 80f, height - 80f));
            GUILayout.Label("Word Arena", titleStyle, GUILayout.Height(72f));
            GUILayout.Label(BannerText(), bannerStyle, GUILayout.Height(58f));
            GUILayout.Label(ScoreText(), hudStyle, GUILayout.Height(48f));
            GUILayout.Label(ComboText(), hudStyle, GUILayout.Height(42f));
            DrawConnectionPanel();
            GUILayout.Space(16f);

            DrawBoard();

            GUILayout.Space(16f);
            GUILayout.Label(selected.Count == 0 ? "Tap cells to spell a word path" : "Selected: " + CurrentWord(), hudStyle, GUILayout.Height(50f));
            GUILayout.Label(status, statusStyle, GUILayout.Height(96f));
            GUILayout.FlexibleSpace();
            DrawActions();
            GUILayout.EndArea();

            GUI.backgroundColor = oldBackground;
            GUI.matrix = oldMatrix;
        }

        private void DrawConnectionPanel()
        {
            GUILayout.Label(ConnectionText(), statusStyle, GUILayout.Height(54f));
            GUILayout.Label(readySummary + "   |   " + resultSummary, statusStyle, GUILayout.Height(54f));
            GUILayout.BeginHorizontal();
            GUILayout.Label("Server", hudStyle, GUILayout.Width(130f), GUILayout.Height(48f));
            serverUrl = GUILayout.TextField(serverUrl, inputStyle, GUILayout.Height(48f));
            GUILayout.Label("Lang", hudStyle, GUILayout.Width(90f), GUILayout.Height(48f));
            language = GUILayout.TextField(language, inputStyle, GUILayout.Width(90f), GUILayout.Height(48f));
            GUILayout.EndHorizontal();
        }

        private void DrawBoard()
        {
            var columns = 4;
            var rows = Mathf.CeilToInt(cells.Count / (float)columns);
            for (var row = 0; row < rows; row++)
            {
                GUILayout.BeginHorizontal();
                for (var col = 0; col < columns; col++)
                {
                    var index = row * columns + col;
                    if (index >= cells.Count)
                    {
                        GUILayout.Space(238f);
                        continue;
                    }

                    var cell = cells[index];
                    GUI.backgroundColor = CellColor(cell);
                    if (GUILayout.Button(CellLabel(cell), cellStyle, GUILayout.Width(238f), GUILayout.Height(150f)))
                    {
                        OnCellTapped(cell.CellId);
                    }
                }

                GUILayout.EndHorizontal();
                GUILayout.Space(14f);
            }
        }

        private void DrawActions()
        {
            GUILayout.BeginHorizontal();
            DrawActionButton(serverMode ? "Send selected" : "Claim selected", OnClaimSelected, new Color32(63, 137, 255, 255));
            DrawActionButton("Switch seat", OnSwitchSeat, new Color32(255, 142, 63, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton("Clear path", OnClearSelection, new Color32(90, 105, 128, 255));
            DrawActionButton("Sudden Death", OnToggleSuddenDeath, new Color32(155, 84, 255, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(createInProgress ? "Creating..." : "Create server match", OnCreateServerMatch, new Color32(54, 172, 118, 255));
            DrawActionButton(readyCheckInProgress ? "Checking ready..." : "Ready check", OnReadyCheck, new Color32(72, 150, 170, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(serverMode ? "Reconnect seat" : "Reset demo", serverMode ? (Action)ReconnectActiveSeat : ResetDemoState, new Color32(96, 112, 146, 255));
            DrawActionButton(tokenRotationInProgress ? "Rotating..." : "Rotate token", OnRotateActiveToken, new Color32(190, 128, 60, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(resultFetchInProgress ? "Fetching result..." : "Fetch result", OnFetchResult, new Color32(80, 120, 210, 255));
            DrawActionButton("Local demo reset", ResetDemoState, new Color32(90, 105, 128, 255));
            GUILayout.EndHorizontal();
        }

        private void DrawActionButton(string label, Action action, Color32 color)
        {
            GUI.backgroundColor = color;
            if (GUILayout.Button(label, actionStyle, GUILayout.Height(74f)))
            {
                action();
            }
        }

        private void OnCellTapped(int cellId)
        {
            if (selected.Contains(cellId))
            {
                selected.Remove(cellId);
            }
            else
            {
                selected.Add(cellId);
            }
        }

        private void OnClaimSelected()
        {
            if (serverMode)
            {
                SubmitSelectedToServer();
                return;
            }

            ApplyLocalDemoClaim();
        }

        private void SubmitSelectedToServer()
        {
            if (liveMatchId == 0)
            {
                SetStatus("No live match id. Create a server match first.");
                return;
            }

            if (network == null || !network.IsConnected)
            {
                SetStatus("Not connected. Reconnecting active seat before submit.");
                ReconnectActiveSeat();
                return;
            }

            if (selected.Count < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            clientSequence++;
            var submitCells = selected.ToArray();
            selected.Clear();
            network.SubmitWord(liveMatchId, clientSequence, submitCells);
            SetStatus("Submitted intent #" + clientSequence + " for '" + WordForCells(submitCells) + "'; waiting for authoritative event/snapshot.");
        }

        private void ApplyLocalDemoClaim()
        {
            var word = CurrentWord();
            if (word.Length < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            if (!DemoWords.Contains(word))
            {
                SetStatus("'" + word + "' rejected in local UX demo. Production validity is server-side only.");
                selected.Clear();
                return;
            }

            var stole = false;
            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell == null)
                {
                    continue;
                }

                if (cell.Locked && cell.OwnerSeat != activeSeat)
                {
                    SetStatus("Cell " + cell.CellId + " is locked for " + cell.LockRemaining.ToString("0.0") + "s; wait before a Cross-Steal.");
                    return;
                }
            }

            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell == null)
                {
                    continue;
                }

                if (cell.OwnerSeat >= 0 && cell.OwnerSeat != activeSeat)
                {
                    stole = true;
                }

                cell.OwnerSeat = activeSeat;
                cell.Locked = true;
                cell.LockRemaining = LockSeconds;
            }

            ApplyDemoScore(word, stole);
            selected.Clear();
            SetStatus(stole
                ? players[activeSeat].Name + " cross-stole '" + word + "'. Server event will be authoritative in live mode."
                : players[activeSeat].Name + " claimed '" + word + "' and locked the cells for 3s.");
        }

        private void OnSwitchSeat()
        {
            activeSeat = 1 - activeSeat;
            selected.Clear();
            if (serverMode)
            {
                ReconnectActiveSeat();
                return;
            }

            SetStatus("Active seat: " + players[activeSeat].Name + ". Shared board remains identical for both players.");
        }

        private void OnClearSelection()
        {
            selected.Clear();
            SetStatus("Selection cleared.");
        }

        private void OnToggleSuddenDeath()
        {
            suddenDeath = !suddenDeath;
            SetStatus(suddenDeath
                ? "Sudden Death presentation on: first accepted server word wins the tiebreak."
                : "Sudden Death presentation off: tied matches render as draws.");
        }


        private void OnReadyCheck()
        {
            if (readyCheckInProgress || network == null)
            {
                return;
            }

            readyCheckInProgress = true;
            StartCoroutine(network.CheckReadyz(serverUrl, HandleReadyzResult));
        }

        private void HandleReadyzResult(ReadyzResult result)
        {
            readyCheckInProgress = false;
            readySummary = result == null ? "Ready: check failed" : "Ready: " + result.DisplayText;
            SetStatus(readySummary);
        }

        private void OnRotateActiveToken()
        {
            if (!serverMode || liveMatchId == 0 || liveTokens == null || activeSeat < 0 || activeSeat >= liveTokens.Length || string.IsNullOrEmpty(liveTokens[activeSeat]))
            {
                SetStatus("No active server seat to rotate. Create a server match first.");
                return;
            }

            if (tokenRotationInProgress || network == null)
            {
                return;
            }

            tokenRotationInProgress = true;
            StartCoroutine(network.RotateSeatToken(serverUrl, liveMatchId, liveTokens[activeSeat], HandleRotateTokenResult));
        }

        private void HandleRotateTokenResult(RotateTokenResult result)
        {
            tokenRotationInProgress = false;
            if (result == null || !result.Success)
            {
                SetStatus(result == null ? "Token rotation failed." : result.Error);
                return;
            }

            if (result.Seat >= 0 && result.Seat < liveTokens.Length)
            {
                liveTokens[result.Seat] = result.Token;
            }

            if (result.Seat >= 0 && result.Seat < liveUserIds.Length && result.UserId != 0)
            {
                liveUserIds[result.Seat] = result.UserId;
                players[result.Seat].UserId = result.UserId;
            }

            SetStatus("Rotated " + players[Mathf.Clamp(result.Seat, 0, players.Length - 1)].DisplayName + " session token; reconnecting with fresh credentials.");
            if (result.Seat == activeSeat)
            {
                ReconnectActiveSeat();
            }
        }

        private void OnFetchResult()
        {
            if (!serverMode || liveMatchId == 0)
            {
                SetStatus("No server match result to fetch yet.");
                return;
            }

            FetchResultOnce(false);
        }

        private void FetchResultOnce(bool terminalSnapshot)
        {
            if (resultFetchInProgress || network == null || liveMatchId == 0)
            {
                return;
            }

            if (terminalSnapshot && terminalResultFetchRequested)
            {
                return;
            }

            terminalResultFetchRequested = terminalSnapshot || terminalResultFetchRequested;
            resultFetchInProgress = true;
            StartCoroutine(network.FetchResult(serverUrl, liveMatchId, HandleMatchResult));
        }

        private void HandleMatchResult(MatchResultSummary result)
        {
            resultFetchInProgress = false;
            resultSummary = result == null ? "Result: fetch failed" : "Result: " + result.DisplayText;
            SetStatus(resultSummary);
        }

        private void OnCreateServerMatch()
        {
            if (createInProgress || network == null)
            {
                return;
            }

            createInProgress = true;
            selected.Clear();
            StartCoroutine(network.CreateMatch(serverUrl, SanitizedLanguage(), suddenDeath, HandleCreateMatchResult));
        }

        private void HandleCreateMatchResult(CreateMatchResult result)
        {
            createInProgress = false;
            if (result == null || !result.Success)
            {
                SetStatus(result == null ? "Create match failed." : result.Error);
                return;
            }

            serverMode = true;
            liveMatchId = result.MatchId;
            liveSeed = result.Seed;
            suddenDeath = result.SuddenDeath;
            liveTokens = result.Tokens;
            liveUserIds = result.UserIds;
            for (var index = 0; index < players.Length && index < liveUserIds.Length; index++)
            {
                players[index].UserId = liveUserIds[index];
            }

            activeSeat = 0;
            clientSequence = 0;
            terminalResultFetchRequested = false;
            resultSummary = "Result: pending for match " + liveMatchId;
            ReconnectActiveSeat();
        }

        private void ReconnectActiveSeat()
        {
            if (network == null || liveTokens == null || activeSeat < 0 || activeSeat >= liveTokens.Length || string.IsNullOrEmpty(liveTokens[activeSeat]))
            {
                SetStatus("No token for active seat. Create a server match first.");
                return;
            }

            network.Connect(serverUrl, liveMatchId, liveTokens[activeSeat]);
            SetStatus("Connecting " + players[activeSeat].Name + " to match " + liveMatchId + ".");
        }

        private void ApplyServerSnapshot(WordArenaSnapshot snapshot)
        {
            if (snapshot == null)
            {
                return;
            }

            serverMode = true;
            liveMatchId = snapshot.MatchId == 0 ? liveMatchId : snapshot.MatchId;
            lastServerTick = snapshot.ServerTick;
            lastStateVersion = snapshot.StateVersion;

            for (var index = 0; index < snapshot.Players.Count && index < players.Length; index++)
            {
                var player = snapshot.Players[index];
                players[index].UserId = player.UserId;
                if (index < liveUserIds.Length && liveUserIds[index] == 0)
                {
                    liveUserIds[index] = player.UserId;
                }

                players[index].Score = (int)player.Score;
                players[index].Combo = Mathf.Max(1f, player.ComboMultiplier);
                players[index].Eliminated = player.IsEliminated;
                players[index].Rank = (int)player.RankPosition;
            }

            var ordered = new List<WordArenaBoardCell>(snapshot.Cells);
            ordered.Sort((left, right) => left.CellId.CompareTo(right.CellId));
            cells.Clear();
            for (var index = 0; index < ordered.Count; index++)
            {
                var remote = ordered[index];
                var cell = new CellViewModel((int)remote.CellId, remote.Letter);
                cell.OwnerSeat = SeatForUser(remote.OwnerUserId);
                cell.Locked = remote.IsLocked;
                cell.LockRemaining = remote.LockRemainingMs / 1000f;
                cells.Add(cell);
            }

            if (snapshot.Over)
            {
                SetStatus("Match over. Final authoritative score: " + ScoreText());
                FetchResultOnce(true);
            }
        }

        private void ApplyServerEvent(WordArenaValidatedEvent wordEvent)
        {
            if (wordEvent == null)
            {
                return;
            }

            lastServerTick = wordEvent.ServerTick;
            if (wordEvent.StateVersion > lastStateVersion)
            {
                lastStateVersion = wordEvent.StateVersion;
            }

            var seat = SeatForUser(wordEvent.UserId);
            if (seat >= 0 && seat < players.Length)
            {
                players[seat].Score = (int)wordEvent.TotalScore;
                players[seat].Combo = Mathf.Max(1f, wordEvent.ComboMultiplier);
            }

            SetStatus("Server event #" + wordEvent.EventId + " seq=" + wordEvent.ClientSequence + ": "
                + ResultText(wordEvent.Result) + " '" + wordEvent.NormalizedWord + "'"
                + " +" + wordEvent.ScoreAdded + (wordEvent.IsSteal ? " Cross-Steal" : string.Empty));
        }

        private void ApplyDemoScore(string word, bool stole)
        {
            var now = Time.time;
            var player = players[activeSeat];
            player.Combo = now - lastAcceptedAt <= ComboWindowSeconds ? player.Combo + 1f : 1f;
            lastAcceptedAt = now;

            var comboBonus = Mathf.Max(0, Mathf.RoundToInt(player.Combo) - 1);
            var stealBonus = stole ? 2 : 0;
            var suddenBonus = suddenDeath ? 1 : 0;
            player.Score += word.Length + comboBonus + stealBonus + suddenBonus;
        }

        private void TickVisibleLocks()
        {
            var changed = false;
            for (var index = 0; index < cells.Count; index++)
            {
                var cell = cells[index];
                if (!cell.Locked)
                {
                    continue;
                }

                cell.LockRemaining = Mathf.Max(0f, cell.LockRemaining - Time.deltaTime);
                if (cell.LockRemaining <= 0f)
                {
                    cell.Locked = false;
                    changed = true;
                }
            }

            if (changed && !serverMode)
            {
                SetStatus("Locks expired: owned cells can now be cross-stolen in the demo.");
            }
        }

        private string CurrentWord()
        {
            var result = string.Empty;
            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell != null)
                {
                    result += cell.Letter;
                }
            }

            return result;
        }

        private string WordForCells(int[] cellIds)
        {
            var result = string.Empty;
            if (cellIds == null)
            {
                return result;
            }

            for (var index = 0; index < cellIds.Length; index++)
            {
                var cell = FindCell(cellIds[index]);
                if (cell != null)
                {
                    result += cell.Letter;
                }
            }

            return result;
        }

        private string CellLabel(CellViewModel cell)
        {
            var owner = cell.OwnerSeat < 0 ? "Free" : players[cell.OwnerSeat].Name;
            var lockText = cell.Locked ? "\nLOCK " + cell.LockRemaining.ToString("0.0") + "s" : string.Empty;
            var selectedText = selected.Contains(cell.CellId) ? "\nSELECTED" : string.Empty;
            return cell.Letter + "\n" + owner + lockText + selectedText;
        }

        private Color32 CellColor(CellViewModel cell)
        {
            if (selected.Contains(cell.CellId))
            {
                return new Color32(248, 210, 88, 255);
            }

            if (cell.OwnerSeat == 0)
            {
                return cell.Locked ? new Color32(55, 127, 230, 255) : new Color32(42, 93, 166, 255);
            }

            if (cell.OwnerSeat == 1)
            {
                return cell.Locked ? new Color32(232, 119, 50, 255) : new Color32(166, 83, 38, 255);
            }

            return new Color32(54, 67, 91, 255);
        }

        private string BannerText()
        {
            if (serverMode)
            {
                return suddenDeath ? "LIVE SERVER — SUDDEN DEATH ENABLED" : "LIVE SERVER — canonical snapshots/events";
            }

            return suddenDeath ? "DEMO SUDDEN DEATH — first accepted server word wins" : "Shared board UX prototype";
        }

        private string ScoreText()
        {
            return players[0].DisplayName + " " + players[0].Score + "  —  " + players[1].Score + " " + players[1].DisplayName
                + "   | active: " + players[activeSeat].DisplayName;
        }

        private string ComboText()
        {
            return "Combo: " + players[0].DisplayName + " x" + Mathf.Max(1f, players[0].Combo).ToString("0.##")
                + " / " + players[1].DisplayName + " x" + Mathf.Max(1f, players[1].Combo).ToString("0.##");
        }

        private string ConnectionText()
        {
            if (!serverMode)
            {
                return "Mode: local presentation demo. Server mode uses REST create + binary protobuf WebSocket.";
            }

            var connected = network != null && network.IsConnected ? "connected" : "disconnected";
            return "Mode: server " + connected + " | match=" + liveMatchId + " seed=" + liveSeed
                + " tick=" + lastServerTick + " version=" + lastStateVersion;
        }

        private string ResultText(WordArenaWordResult result)
        {
            switch (result)
            {
                case WordArenaWordResult.Accepted:
                    return "accepted";
                case WordArenaWordResult.RejectedNotInDictionary:
                    return "rejected: not in dictionary";
                case WordArenaWordResult.AlreadyClaimed:
                    return "rejected: already claimed";
                case WordArenaWordResult.BlockedByRule:
                    return "blocked by rule";
                case WordArenaWordResult.InvalidInput:
                    return "invalid input";
                case WordArenaWordResult.MatchNotActive:
                    return "match not active";
                default:
                    return "unspecified";
            }
        }

        private string SanitizedLanguage()
        {
            var value = string.IsNullOrEmpty(language) ? "en" : language.Trim().ToLowerInvariant();
            if (value != "en" && value != "ru" && value != "uk")
            {
                return "en";
            }

            return value;
        }

        private int SeatForUser(ulong userId)
        {
            if (userId == 0)
            {
                return -1;
            }

            for (var index = 0; index < liveUserIds.Length; index++)
            {
                if (liveUserIds[index] == userId)
                {
                    return index;
                }
            }

            for (var index = 0; index < players.Length; index++)
            {
                if (players[index].UserId == userId)
                {
                    return index;
                }
            }

            return -1;
        }

        private CellViewModel FindCell(int cellId)
        {
            for (var index = 0; index < cells.Count; index++)
            {
                if (cells[index].CellId == cellId)
                {
                    return cells[index];
                }
            }

            return null;
        }

        private void ResetDemoState()
        {
            if (network != null)
            {
                network.Disconnect();
            }

            serverMode = false;
            createInProgress = false;
            readyCheckInProgress = false;
            tokenRotationInProgress = false;
            resultFetchInProgress = false;
            terminalResultFetchRequested = false;
            liveMatchId = 0;
            liveSeed = 0;
            lastServerTick = 0;
            lastStateVersion = 0;
            clientSequence = 0;
            liveTokens = new string[2];
            liveUserIds = new ulong[2];
            activeSeat = 0;
            selected.Clear();
            cells.Clear();
            for (var index = 0; index < DemoLetters.Length; index++)
            {
                cells.Add(new CellViewModel(index, DemoLetters[index]));
            }

            players[0].Reset("Blue");
            players[1].Reset("Orange");
            readySummary = "Ready: not checked";
            resultSummary = "Result: not fetched";
            status = "Demo board ready. Use Create server match to bind this UI to the authoritative backend.";
        }

        private void SetStatus(string message)
        {
            status = message;
        }

        private void EnqueueOnMainThread(Action action)
        {
            lock (mainThreadActionsLock)
            {
                mainThreadActions.Enqueue(action);
            }
        }

        private void DrainMainThreadActions()
        {
            while (true)
            {
                Action action;
                lock (mainThreadActionsLock)
                {
                    if (mainThreadActions.Count == 0)
                    {
                        return;
                    }

                    action = mainThreadActions.Dequeue();
                }

                action();
            }
        }

        private void EnsureStyles()
        {
            if (titleStyle != null)
            {
                return;
            }

            titleStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 58,
                fontStyle = FontStyle.Bold,
                normal = { textColor = Color.white }
            };
            bannerStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 32,
                fontStyle = FontStyle.Bold,
                normal = { textColor = new Color32(255, 224, 92, 255) }
            };
            hudStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 29,
                fontStyle = FontStyle.Bold,
                normal = { textColor = Color.white }
            };
            statusStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 23,
                wordWrap = true,
                normal = { textColor = new Color32(218, 228, 244, 255) }
            };
            cellStyle = new GUIStyle(GUI.skin.button)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 25,
                fontStyle = FontStyle.Bold,
                wordWrap = true
            };
            actionStyle = new GUIStyle(GUI.skin.button)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 27,
                fontStyle = FontStyle.Bold,
                wordWrap = true
            };
            inputStyle = new GUIStyle(GUI.skin.textField)
            {
                alignment = TextAnchor.MiddleLeft,
                fontSize = 24,
                wordWrap = false
            };
        }

        private sealed class CellViewModel
        {
            public CellViewModel(int cellId, string letter)
            {
                CellId = cellId;
                Letter = letter;
                OwnerSeat = -1;
            }

            public int CellId { get; private set; }
            public string Letter { get; private set; }
            public int OwnerSeat { get; set; }
            public bool Locked { get; set; }
            public float LockRemaining { get; set; }
        }

        private sealed class PlayerViewModel
        {
            public PlayerViewModel(string name)
            {
                Reset(name);
            }

            public string Name { get; private set; }
            public ulong UserId { get; set; }
            public int Score { get; set; }
            public float Combo { get; set; }
            public int Rank { get; set; }
            public bool Eliminated { get; set; }

            public string DisplayName
            {
                get
                {
                    return UserId == 0 ? Name : Name + " #" + UserId;
                }
            }

            public void Reset(string name)
            {
                Name = name;
                UserId = 0;
                Score = 0;
                Combo = 1f;
                Rank = 0;
                Eliminated = false;
            }
        }
    }
}
