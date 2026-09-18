const config = require("../release-rc.config.cjs");

function validateVersion(branch, version) {
  const active = config.branches.find((entry) => entry.prerelease === "rc");
  if (branch !== active.name) {
    throw new Error("RC publishing is allowed only on the configured release branch");
  }
  const target = branch.slice("release/".length);
  const prefix = `${target}-rc.`;
  if (typeof version !== "string" || !version.startsWith(prefix)) {
    throw new Error("Calculated RC version does not match the release branch target");
  }
  const number = version.slice(prefix.length);
  const match = /^[1-9][0-9]*$/.exec(number);
  if (!match || match[0] !== number) {
    throw new Error("RC version must end with a positive integer without leading zeros");
  }
}

function verifyRelease(_pluginConfig, { branch, nextRelease }) {
  validateVersion(branch.name, nextRelease.version);
}

module.exports = { validateVersion, verifyRelease };

if (require.main === module) {
  try {
    validateVersion(process.env.GITHUB_REF_NAME, process.env.RC_VERSION);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
