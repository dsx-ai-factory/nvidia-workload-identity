module.exports = {
  branches: [
    "master",
    { name: "release/1.15.0", channel: "rc", prerelease: "rc" },
  ],
  tagFormat: "v${version}",
  plugins: [
    ["@semantic-release/commit-analyzer", { preset: "conventionalcommits" }],
    ["@semantic-release/release-notes-generator", { preset: "conventionalcommits" }],
    "./.github/rc/validate-version.cjs",
    ["@semantic-release/github", { successComment: false, failComment: false }],
  ],
};
