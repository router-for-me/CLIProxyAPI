/** Match yaml.v3's typed bool decoder, including its YAML 1.1 string spellings. */
export function readConfigBoolean(value: unknown, fallback: boolean): boolean {
  if (typeof value === 'boolean') return value;
  switch (value) {
    case 'y':
    case 'Y':
    case 'yes':
    case 'Yes':
    case 'YES':
    case 'on':
    case 'On':
    case 'ON':
      return true;
    case 'n':
    case 'N':
    case 'no':
    case 'No':
    case 'NO':
    case 'off':
    case 'Off':
    case 'OFF':
      return false;
    default:
      return fallback;
  }
}
