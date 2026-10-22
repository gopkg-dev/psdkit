export const modulePath = 'github.com/gopkg-dev/psdkit';
export const repository = 'gopkg-dev/psdkit';

export function parseVersion(tag) {
  const match = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/.exec(tag);
  if (!match || match[0] !== tag || match[4]?.split('.').some(part => /^0\d+$/.test(part))) {
    throw new Error(`无效版本 ${JSON.stringify(tag)}；需要 vX.Y.Z 或 vX.Y.Z-rc.1，不使用构建元数据。`);
  }
  return { tag, numbers: match.slice(1, 4).map(BigInt), prerelease: match[4]?.split('.') ?? [] };
}

export function compareVersions(left, right) {
  const a = parseVersion(left);
  const b = parseVersion(right);
  for (let index = 0; index < 3; index++) {
    if (a.numbers[index] !== b.numbers[index]) return a.numbers[index] > b.numbers[index] ? 1 : -1;
  }
  if (!a.prerelease.length || !b.prerelease.length) {
    return Number(!a.prerelease.length) - Number(!b.prerelease.length);
  }
  for (let index = 0; index < Math.max(a.prerelease.length, b.prerelease.length); index++) {
    const x = a.prerelease[index];
    const y = b.prerelease[index];
    if (x === undefined || y === undefined) return Number(x !== undefined) - Number(y !== undefined);
    if (x === y) continue;
    const xNumeric = /^\d+$/.test(x);
    const yNumeric = /^\d+$/.test(y);
    if (xNumeric && yNumeric) return BigInt(x) > BigInt(y) ? 1 : -1;
    if (xNumeric !== yNumeric) return xNumeric ? -1 : 1;
    return x > y ? 1 : -1;
  }
  return 0;
}

export function previousVersion(tag, tags) {
  const current = parseVersion(tag);
  return tags.filter(candidate => {
    try {
      const parsed = parseVersion(candidate);
      return compareVersions(candidate, tag) < 0 &&
        (current.prerelease.length > 0 || parsed.prerelease.length === 0);
    } catch { return false; }
  }).sort(compareVersions).at(-1) ?? '';
}
