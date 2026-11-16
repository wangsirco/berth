// guacamole-common-js ships no types; the remote desktop viewer is the only user.
declare module 'guacamole-common-js' {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const Guacamole: any
  export default Guacamole
}
