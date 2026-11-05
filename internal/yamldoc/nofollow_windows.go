package yamldoc

// oNoFollow is 0: Windows has no O_NOFOLLOW, so ReadNoFollow relies on its os.Lstat and
// os.SameFile checks alone.
const oNoFollow = 0
