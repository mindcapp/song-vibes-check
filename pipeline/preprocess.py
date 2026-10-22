"""Cleaning, normalisation and PCA; fitted params are persisted for new data."""
import logging
from dataclasses import dataclass
from typing import Any, Tuple

import joblib
import numpy as np
import pandas as pd
from sklearn.decomposition import PCA
from sklearn.preprocessing import QuantileTransformer, RobustScaler, StandardScaler

log = logging.getLogger(__name__)

RANDOM_STATE = 42

SCALERS = ("standard", "robust", "quantile")


def make_scaler(name: str) -> Any:
    """Feature normaliser applied before PCA."""
    if name == "standard":
        return StandardScaler()
    if name == "robust":
        return RobustScaler()
    if name == "quantile":
        return QuantileTransformer(
            n_quantiles=1000, output_distribution="normal", random_state=RANDOM_STATE
        )
    raise ValueError("unknown scaler %r, expected one of %s" % (name, SCALERS))


def drop_non_finite(features: pd.DataFrame) -> pd.DataFrame:
    """Drop rows containing NaN or +-inf."""
    mask = np.isfinite(features.to_numpy(dtype=np.float64)).all(axis=1)
    dropped = int((~mask).sum())
    if dropped:
        log.warning("dropping %d tracks with NaN/inf features", dropped)
    return features.loc[mask]


@dataclass
class Preprocessor:
    scaler: Any
    pca: PCA

    def transform(self, x: np.ndarray) -> np.ndarray:
        """Apply the fitted scaler + PCA to raw feature rows (n, n_features)."""
        return self.pca.transform(self.scaler.transform(x)).astype(np.float32)

    def save(self, path: str) -> None:
        joblib.dump({"scaler": self.scaler, "pca": self.pca}, path)

    @classmethod
    def load(cls, path: str) -> "Preprocessor":
        params = joblib.load(path)
        return cls(scaler=params["scaler"], pca=params["pca"])


def fit_preprocessor(
    x: np.ndarray, n_components: int = 64, scaler: str = "standard"
) -> Tuple[Preprocessor, np.ndarray]:
    """Fit the scaler and PCA on x; return the preprocessor and PCA vectors."""
    fitted = make_scaler(scaler).fit(x)
    pca = PCA(n_components=n_components, random_state=RANDOM_STATE).fit(fitted.transform(x))
    log.info(
        "%s + PCA %d -> %d dims, explained variance %.3f",
        scaler, x.shape[1], n_components, pca.explained_variance_ratio_.sum(),
    )
    pre = Preprocessor(scaler=fitted, pca=pca)
    return pre, pre.transform(x)
