using System;
using System.IO;
using UnityEditor;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;
using UnityEngine;

namespace Words
{
    /// <summary>
    /// Deterministic, non-interactive Android entry point for CI and local Editor use.
    /// </summary>
    public static class BuildCommand
    {
        private const string ScenePath = "Assets/Scenes/Main.unity";
        private const string DefaultBuildPath = "Builds/Android/words-debug.apk";

        public static void BuildAndroid()
        {
            try
            {
                var buildType = GetArgument("-buildType", Environment.GetEnvironmentVariable("WORDS_BUILD_TYPE") ?? "debug").ToLowerInvariant();
                var buildPath = GetArgument("-buildPath", Environment.GetEnvironmentVariable("WORDS_BUILD_PATH") ?? DefaultBuildPath);

                if (buildType != "debug" && buildType != "release")
                {
                    throw new ArgumentException("-buildType must be either 'debug' or 'release'.");
                }

                if (!File.Exists(Path.Combine(Directory.GetCurrentDirectory(), ScenePath)))
                {
                    throw new FileNotFoundException("Required build scene was not found.", ScenePath);
                }

                EditorUserBuildSettings.SwitchActiveBuildTarget(BuildTargetGroup.Android, BuildTarget.Android);
                EditorBuildSettings.scenes = new[]
                {
                    new EditorBuildSettingsScene(ScenePath, true)
                };

                PlayerSettings.companyName = "JoTalbot";
                PlayerSettings.productName = "Word Arena";
                PlayerSettings.applicationIdentifier = "com.jotalbot.words";
                PlayerSettings.bundleVersion = "0.1.0";
                PlayerSettings.Android.bundleVersionCode = 1;
                PlayerSettings.Android.forceInternetPermission = true;
                PlayerSettings.SetScriptingBackend(NamedBuildTarget.Android, ScriptingImplementation.IL2CPP);
                PlayerSettings.Android.targetArchitectures = buildType == "debug"
                    ? AndroidArchitecture.ARM64 | AndroidArchitecture.X86_64
                    : AndroidArchitecture.ARM64;
                EditorUserBuildSettings.buildAppBundle = false;

                ApplyNetworkSecurityConfig(buildType);

                var outputPath = Path.GetFullPath(buildPath);
                var outputDirectory = Path.GetDirectoryName(outputPath);
                if (!string.IsNullOrEmpty(outputDirectory))
                {
                    Directory.CreateDirectory(outputDirectory);
                }

                var options = BuildOptions.StrictMode | BuildOptions.CompressWithLz4HC;
                if (buildType == "debug")
                {
                    options |= BuildOptions.Development | BuildOptions.AllowDebugging;
                }

                var report = BuildPipeline.BuildPlayer(new BuildPlayerOptions
                {
                    scenes = new[] { ScenePath },
                    locationPathName = outputPath,
                    target = BuildTarget.Android,
                    targetGroup = BuildTargetGroup.Android,
                    options = options
                });

                if (report.summary.result != BuildResult.Succeeded)
                {
                    throw new BuildFailedException(
                        $"Android build failed: {report.summary.result}; errors={report.summary.totalErrors}; warnings={report.summary.totalWarnings}");
                }

                Debug.Log($"Word Arena Android build succeeded: {outputPath} ({report.summary.totalSize} bytes)");
                EditorApplication.Exit(0);
            }
            catch (Exception exception)
            {
                Debug.LogException(exception);
                EditorApplication.Exit(1);
            }
        }

        /// <summary>
        /// Batch 34D: UnityWebRequest on Android enforces the GLOBAL platform
        /// cleartext policy - its Java pre-check calls
        /// NetworkSecurityPolicy.isCleartextTrafficPermitted() WITHOUT a
        /// hostname, so a domain-scoped exception for the emulator host alias
        /// never survives it. Measured twice with the merged config verifiably
        /// packaged in the APK (runs 34906844282 and 34912557224: enqueue to
        /// http://10.0.2.2:18080 still threw "Insecure connection not
        /// allowed"). Debug builds therefore get cleartext ENABLED globally
        /// (development images only - CI device automation plays against a
        /// runner-local server over the AOSP host alias); the committed
        /// release configuration keeps cleartext DENIED for every origin.
        /// The rewrite happens in the ephemeral CI checkout; a local editor
        /// debug build dirties the tracked config file - restore it with git
        /// checkout afterwards.
        /// </summary>
        private static void ApplyNetworkSecurityConfig(string buildType)
        {
            const string netSecPath =
                "Assets/Plugins/WordArenaNetSec.androidlib/res/xml/network_security_config.xml";
            var permitted = buildType == "debug" ? "true" : "false";
            var xml = "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n"
                + "<!-- Written by BuildCommand.ApplyNetworkSecurityConfig (batch 34D);"
                + " committed state is the strict release variant. -->\n"
                + "<network-security-config>\n"
                + $"    <base-config cleartextTrafficPermitted=\"{permitted}\" />\n"
                + "</network-security-config>\n";
            File.WriteAllText(netSecPath, xml);
            AssetDatabase.ImportAsset(netSecPath, ImportAssetOptions.ForceUpdate);
            AssetDatabase.Refresh();
            Debug.Log($"Word Arena: network security config written (cleartext permitted = {permitted})");
        }

        private static string GetArgument(string name, string fallback)
        {
            var args = Environment.GetCommandLineArgs();
            for (var index = 0; index < args.Length - 1; index++)
            {
                if (string.Equals(args[index], name, StringComparison.OrdinalIgnoreCase))
                {
                    return args[index + 1];
                }
            }

            return fallback;
        }
    }
}
