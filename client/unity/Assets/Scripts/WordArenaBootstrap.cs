using System;
using System.Collections.Generic;
using UnityEngine;

namespace Words.Client
{
    /// <summary>
    /// M1 client UX bootstrap. This is a non-authoritative presentation/demo
    /// harness: it visualizes the shared board, claim/lock/cross-steal states,
    /// combo and Sudden Death banners while the server remains the only source
    /// of competitive truth.
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

        private GUIStyle titleStyle;
        private GUIStyle bannerStyle;
        private GUIStyle hudStyle;
        private GUIStyle statusStyle;
        private GUIStyle cellStyle;
        private GUIStyle actionStyle;
        private int activeSeat;
        private bool suddenDeath;
        private float lastAcceptedAt = -999f;
        private string status = "Demo board ready. Server-authoritative networking is the next client slice.";

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
            for (var index = 0; index < DemoLetters.Length; index++)
            {
                cells.Add(new CellViewModel(index, DemoLetters[index]));
            }
        }

        private void Update()
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

            if (changed)
            {
                SetStatus("Locks expired: owned cells can now be cross-stolen in the demo.");
            }
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
            GUILayout.Label("Word Arena", titleStyle, GUILayout.Height(78f));
            GUILayout.Label(suddenDeath ? "SUDDEN DEATH — first accepted server word wins" : "Shared board UX prototype", bannerStyle, GUILayout.Height(58f));
            GUILayout.Label($"Blue {players[0].Score}  —  {players[1].Score} Orange   | active: {players[activeSeat].Name}", hudStyle, GUILayout.Height(50f));
            GUILayout.Label($"Combo: Blue x{Math.Max(1, players[0].Combo)} / Orange x{Math.Max(1, players[1].Combo)}", hudStyle, GUILayout.Height(44f));
            GUILayout.Space(20f);

            DrawBoard();

            GUILayout.Space(18f);
            GUILayout.Label(selected.Count == 0 ? "Tap cells to spell CAT or DOG" : $"Selected: {CurrentWord()}", hudStyle, GUILayout.Height(52f));
            GUILayout.Label(status, statusStyle, GUILayout.Height(96f));
            GUILayout.FlexibleSpace();
            DrawActions();
            GUILayout.EndArea();

            GUI.backgroundColor = oldBackground;
            GUI.matrix = oldMatrix;
        }

        private void DrawBoard()
        {
            for (var row = 0; row < 3; row++)
            {
                GUILayout.BeginHorizontal();
                for (var col = 0; col < 4; col++)
                {
                    var index = row * 4 + col;
                    var cell = cells[index];
                    GUI.backgroundColor = CellColor(cell);
                    if (GUILayout.Button(CellLabel(cell), cellStyle, GUILayout.Width(238f), GUILayout.Height(150f)))
                    {
                        OnCellTapped(index);
                    }
                }

                GUILayout.EndHorizontal();
                GUILayout.Space(14f);
            }
        }

        private void DrawActions()
        {
            GUILayout.BeginHorizontal();
            DrawActionButton("Claim selected", OnClaimSelected, new Color32(63, 137, 255, 255));
            DrawActionButton("Switch seat", OnSwitchSeat, new Color32(255, 142, 63, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton("Clear path", OnClearSelection, new Color32(90, 105, 128, 255));
            DrawActionButton("Sudden Death", OnToggleSuddenDeath, new Color32(155, 84, 255, 255));
            GUILayout.EndHorizontal();
        }

        private void DrawActionButton(string label, Action action, Color32 color)
        {
            GUI.backgroundColor = color;
            if (GUILayout.Button(label, actionStyle, GUILayout.Height(76f)))
            {
                action();
            }
        }

        private void OnCellTapped(int index)
        {
            if (selected.Contains(index))
            {
                selected.Remove(index);
            }
            else
            {
                selected.Add(index);
            }
        }

        private void OnClaimSelected()
        {
            var word = CurrentWord();
            if (word.Length < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            if (!DemoWords.Contains(word))
            {
                SetStatus($"'{word}' rejected in local UX demo. Production validity is server-side only.");
                selected.Clear();
                return;
            }

            var stole = false;
            foreach (var index in selected)
            {
                var cell = cells[index];
                if (cell.Locked && cell.OwnerSeat != activeSeat)
                {
                    SetStatus($"Cell {index} is locked for {cell.LockRemaining:0.0}s; wait before a Cross-Steal.");
                    return;
                }
            }

            foreach (var index in selected)
            {
                var cell = cells[index];
                if (cell.OwnerSeat >= 0 && cell.OwnerSeat != activeSeat)
                {
                    stole = true;
                }

                cell.OwnerSeat = activeSeat;
                cell.Locked = true;
                cell.LockRemaining = LockSeconds;
            }

            ApplyScore(word, stole);
            selected.Clear();
            SetStatus(stole
                ? $"{players[activeSeat].Name} cross-stole '{word}'. Server event will be authoritative."
                : $"{players[activeSeat].Name} claimed '{word}' and locked the cells for 3s.");
        }

        private void OnSwitchSeat()
        {
            activeSeat = 1 - activeSeat;
            selected.Clear();
            SetStatus($"Active seat: {players[activeSeat].Name}. Shared board remains identical for both players.");
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

        private void ApplyScore(string word, bool stole)
        {
            var now = Time.time;
            var player = players[activeSeat];
            player.Combo = now - lastAcceptedAt <= ComboWindowSeconds ? player.Combo + 1 : 1;
            lastAcceptedAt = now;

            var comboBonus = Math.Max(0, player.Combo - 1);
            var stealBonus = stole ? 2 : 0;
            var suddenBonus = suddenDeath ? 1 : 0;
            player.Score += word.Length + comboBonus + stealBonus + suddenBonus;
        }

        private string CurrentWord()
        {
            var result = string.Empty;
            foreach (var index in selected)
            {
                result += cells[index].Letter;
            }

            return result;
        }

        private string CellLabel(CellViewModel cell)
        {
            var owner = cell.OwnerSeat < 0 ? "Free" : players[cell.OwnerSeat].Name;
            var lockText = cell.Locked ? $"\nLOCK {cell.LockRemaining:0.0}s" : string.Empty;
            var selectedText = selected.Contains(cell.Index) ? "\nSELECTED" : string.Empty;
            return $"{cell.Letter}\n{owner}{lockText}{selectedText}";
        }

        private Color32 CellColor(CellViewModel cell)
        {
            if (selected.Contains(cell.Index))
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

        private void SetStatus(string message)
        {
            status = message;
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
                fontSize = 34,
                fontStyle = FontStyle.Bold,
                normal = { textColor = new Color32(255, 224, 92, 255) }
            };
            hudStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 30,
                fontStyle = FontStyle.Bold,
                normal = { textColor = Color.white }
            };
            statusStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 25,
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
                fontSize = 28,
                fontStyle = FontStyle.Bold,
                wordWrap = true
            };
        }

        private sealed class CellViewModel
        {
            public CellViewModel(int index, string letter)
            {
                Index = index;
                Letter = letter;
                OwnerSeat = -1;
            }

            public int Index { get; }
            public string Letter { get; }
            public int OwnerSeat { get; set; }
            public bool Locked { get; set; }
            public float LockRemaining { get; set; }
        }

        private sealed class PlayerViewModel
        {
            public PlayerViewModel(string name)
            {
                Name = name;
            }

            public string Name { get; }
            public int Score { get; set; }
            public int Combo { get; set; }
        }
    }
}
