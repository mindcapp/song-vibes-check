import numpy as np

from evaluate import nearest_neighbours, same_genre_at_k


def _toy():
    # two tight clusters; artist "a" owns the closest neighbours of track 0
    vectors = np.array([
        [1.0, 0.00], [1.0, 0.01], [1.0, 0.02], [1.0, 0.30], [1.0, 0.31],
        [0.0, 1.00], [0.01, 1.0], [0.02, 1.0],
    ])
    genres = ["Rock"] * 5 + ["Pop"] * 3
    artists = ["a", "a", "a", "b", "c", "d", "e", "f"]
    return vectors, genres, artists


def test_neighbours_exclude_self_and_same_artist():
    vectors, _, artists = _toy()
    plain = nearest_neighbours(vectors, artists, k=2)
    assert list(plain[0]) == [1, 2]
    other = nearest_neighbours(vectors, artists, k=2, candidates=3, exclude_same_artist=True)
    # candidates [1, 2, 3] leave only one other artist -> falls back to full ranking
    assert list(other[0]) == [3, 4]
    assert all(i not in idx for i, idx in enumerate(plain))


def test_same_genre_at_k():
    vectors, genres, artists = _toy()
    scores = same_genre_at_k(vectors, genres, artists, k=2, candidates=7)
    assert scores["all"] == 1.0
    assert 0.0 < scores["other_artist"] <= 1.0
